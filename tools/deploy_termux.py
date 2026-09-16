#!/usr/bin/env python3
"""Install the agent on an Android handset running Termux, over the home LAN.

Bootstrap on the phone, once, by hand in Termux:

    pkg update && pkg install -y openssh && passwd && sshd && ip addr | grep 'inet 192'

Then from this workstation on the same Wi-Fi:

    RNFO_DEPLOY_PASSWORD='...' python tools/deploy_termux.py <phone-ip> ru-mow-home eyeball
    RNFO_DEPLOY_PASSWORD='...' python tools/deploy_termux.py <phone-ip> ru-mobile mobile --max-body 262144

What it does: asks the phone for its CPU architecture, builds the matching
static binary, uploads binary + install script + boot script + the
collector's public key, writes own.csv from .env, runs the installer. The
phone ends up running `rnfo-probe -daemon` under a Termux:Boot supervisor.

DNS: Android has no /etc/resolv.conf, so a static binary cannot resolve
anything by default. For an eyeball probe the resolver is the home router
(default gateway), which keeps the ISP's DNS in the path. Detected from the
phone; override with --dns.
"""
import argparse
import io
import os
import posixpath
import subprocess
import sys
import tarfile

import paramiko

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "tools"))
from deploy import load_env, own_targets_csv  # noqa: E402

# Termux's own architecture decides what binary will run, not the CPU's.
# Budget Android devices ship a 32-bit userspace on a 64-bit core: the POCO
# C51 has a 64-bit Helio G36 but reports armeabi-v7a and runs a 32-bit Termux,
# where an arm64 binary would not execute at all. `dpkg --print-architecture`
# inside Termux is authoritative; uname is only a fallback, and armv8l there
# means a 32-bit userspace on a 64-bit core, so it maps to arm, not arm64.
ARCH = {
    "aarch64": "arm64", "arm64": "arm64",
    "arm": "arm", "armv7l": "arm", "armv8l": "arm",
    "x86_64": "amd64", "i686": "386",
}


def build(goarch: str) -> bytes:
    out = os.path.join(REPO, "bin", f"rnfo-probe-linux-{goarch}")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH=goarch, CGO_ENABLED="0")
    if goarch == "arm":
        env["GOARM"] = "7"
    print(f"==> building linux/{goarch}")
    subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./probe"], cwd=REPO, env=env, check=True)
    with open(out, "rb") as f:
        return f.read()


def run(c, cmd, check=True):
    _, so, se = c.exec_command(cmd, timeout=600)
    out = so.read().decode("utf-8", "replace")
    err = se.read().decode("utf-8", "replace")
    rc = so.channel.recv_exit_status()
    if out.strip():
        print(out.rstrip())
    if err.strip():
        print(err.rstrip(), file=sys.stderr)
    if check and rc != 0:
        raise SystemExit(f"remote command failed (rc={rc}): {cmd}")
    return out


def upload_binary(c, binary: bytes) -> str:
    """Put the agent on the phone early, so resolvers can be tested with it."""
    home = run(c, "echo $HOME").strip()
    path = posixpath.join(home, "rnfo-probe.test")
    run(c, f"mkdir -p {home}/rnfo")
    sftp = c.open_sftp()
    with sftp.file(path, "wb") as f:
        f.write(binary)
    sftp.close()
    run(c, f"chmod 0755 {path}")
    return path


def pick_resolver(c, a) -> str:
    """Return the first candidate resolver that actually resolves names.

    Tested with the agent itself in dry-run mode, on the phone, over the
    network the probe will really use. Anything less is a guess.
    """
    home = run(c, "echo $HOME").strip()
    binpath = posixpath.join(home, "rnfo-probe.test")

    candidates = []
    if a.dns:
        candidates.append(a.dns)
    for prop in ("net.dns1", "dhcp.wlan0.dns1"):
        v = run(c, f"getprop {prop} 2>/dev/null || true", check=False).strip()
        if v and not v.startswith("::"):
            candidates.append(v)
    # The router is the .1 of the phone's own /24 far more often than not, and
    # using it keeps the ISP's resolver in the path, which is what an eyeball
    # measurement should see.
    host = a.lan_ip or a.host
    if host.count(".") == 3 and not host.startswith("127."):
        parts = host.split(".")
        candidates.append(".".join(parts[:3] + ["1"]))
        candidates.append(".".join(parts[:3] + ["254"]))

    seen, ordered = set(), []
    for d in candidates:
        if d not in seen:
            seen.add(d)
            ordered.append(d)

    for d in ordered:
        print(f"    trying resolver {d} ...", end=" ", flush=True)
        out = run(c, f"{binpath} -probe dns-test -net {a.net} -profile controls -dry-run -dns '{d}' 2>&1 | head -4",
                  check=False)
        if " ok " in out or "stage=ok" in out:
            print("resolves")
            return d
        print("no")
    raise SystemExit(
        "no working resolver found on the phone. Pass --dns <router-ip> explicitly; "
        "find it in Android Settings > Wi-Fi > (your network) > Advanced > Gateway.")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("host", help="phone's LAN address")
    ap.add_argument("probe_id")
    ap.add_argument("net", choices=["eyeball", "mobile"])
    ap.add_argument("--port", type=int, default=8022)
    ap.add_argument("--user", default="termux", help="ignored by Termux sshd but required by the protocol")
    ap.add_argument("--password-env", default="RNFO_DEPLOY_PASSWORD")
    ap.add_argument("--dns", default=None, help="resolver host[:port]; default: probed, see pick_resolver")
    ap.add_argument("--lan-ip", default=None,
                    help="the phone's own LAN address, when reaching it through an adb forward "
                         "(127.0.0.1 tells us nothing about its router)")
    ap.add_argument("--max-body", type=int, default=2 << 20, help="body cap per target; use 262144 on a SIM")
    ap.add_argument("--pubkey", default=os.path.expanduser("~/.ssh/id_ed25519.pub"))
    ap.add_argument("--tunnel", default=None, help="jump host for the reverse tunnel, user@host (e.g. rnfo-tunnel@<moscow-vps>)")
    ap.add_argument("--tunnel-port", type=int, default=None, help="loopback port on the jump host this phone binds (one per phone)")
    a = ap.parse_args()

    pw = os.environ.get(a.password_env)
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    try:
        c.connect(a.host, port=a.port, username=a.user, key_filename=os.path.expanduser("~/.ssh/id_ed25519"),
                  look_for_keys=True, allow_agent=True, timeout=20, banner_timeout=25, auth_timeout=25)
    except Exception:  # noqa: BLE001
        if not pw:
            raise SystemExit(f"set {a.password_env} (the password you gave `passwd` in Termux)")
        c.connect(a.host, port=a.port, username=a.user, password=pw, look_for_keys=False,
                  allow_agent=False, timeout=20, banner_timeout=25, auth_timeout=25)
    print(f"==> connected to {a.host}:{a.port}")

    reported = run(c, "dpkg --print-architecture 2>/dev/null || uname -m", check=False).strip().splitlines()
    reported = reported[-1].strip() if reported else ""
    goarch = ARCH.get(reported)
    if not goarch:
        raise SystemExit(f"unknown architecture {reported!r}")
    print(f"==> termux architecture {reported} -> linux/{goarch}")

    egress = run(c, "curl -s --max-time 8 https://ipinfo.io/json 2>/dev/null | tr -d '\\n' || true", check=False).strip()
    print(f"==> phone egress as seen from outside: {egress[:200]}")
    if "AS200823" in egress:
        raise SystemExit("the phone's traffic leaves through the owner's own AS200823 node - it is inside the "
                         "tunnel. A probe there measures the tunnel, not the network: fix the routing first, then deploy.")

    binary = build(goarch)
    upload_binary(c, binary)
    env = load_env()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        def add(name, data, mode):
            info = tarfile.TarInfo(name)
            info.size, info.mode = len(data), mode
            tar.addfile(info, io.BytesIO(data))
        add("rnfo-probe", binary, 0o755)
        for name in ("install.sh", "rnfo-boot.sh", "supervise.sh", "status.sh"):
            with open(os.path.join(REPO, "probe", "deploy", "termux", name), "rb") as f:
                add(name, f.read().replace(b"\r\n", b"\n"), 0o755)
        if os.path.exists(a.pubkey):
            with open(a.pubkey, "rb") as f:
                add("authorized_key", f.read().strip() + b"\n", 0o644)

    dns = pick_resolver(c, a)
    print(f"==> resolver: {dns} (proven, not inferred)")

    home = run(c, "echo $HOME").strip()
    remote = posixpath.join(home, "rnfo-deploy")
    run(c, f"rm -rf {remote} && mkdir -p {remote} $HOME/rnfo")
    sftp = c.open_sftp()
    with sftp.file(posixpath.join(remote, "b.tgz"), "wb") as f:
        f.write(buf.getvalue())
    if env.get("RNFO_RESPONDER_IP"):
        with sftp.file(posixpath.join(home, "rnfo", "own.csv"), "w") as f:
            f.write(own_targets_csv(env["RNFO_RESPONDER_IP"]))
        print("==> own targets written")
    sftp.close()
    run(c, f"cd {remote} && tar xzf b.tgz && rm b.tgz")
    tunnel = f" '{a.tunnel}' {a.tunnel_port}" if a.tunnel and a.tunnel_port else ""
    watch = env.get("RNFO_WATCH_PUBKEY", "")
    prefix = f"WATCH_PUBKEY='{watch}' " if watch else ""
    run(c, f"{prefix}bash {remote}/install.sh {a.probe_id} {a.net} '{dns}' {a.max_body}{tunnel}")
    run(c, f"rm -rf {remote} {home}/rnfo-probe.test")
    c.close()
    print("==> done. Add the phone to probes.yaml (status: active) and .env (RNFO_SSH_...) once its first run record exists.")


if __name__ == "__main__":
    main()
