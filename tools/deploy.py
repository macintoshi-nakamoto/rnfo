#!/usr/bin/env python3
"""Build the agent and install it on a probe.

Every probe gets the same binary and the same units; only the probe id and
network type differ. That is enforced here rather than left to discipline.

Usage:
    python tools/deploy.py <host> <probe-id> <hosting|eyeball|mobile>
                           [--arch amd64|arm64] [--password-env VAR]
                           [--jump user@host] [--run]

Credentials come from the environment or from an SSH agent/key, never from
the command line and never from a file inside the repository.
"""
import argparse
import io
import os
import posixpath
import subprocess
import sys
import tarfile
import time

import paramiko

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RESPONDER_TLS_PORTS = ("443", "8443", "2053", "9443")


def load_env():
    """Read .env (git-ignored). Only the keys the deploy needs."""
    out = {}
    p = os.path.join(REPO, ".env")
    if not os.path.exists(p):
        return out
    for line in open(p, encoding="utf-8"):
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        out[k.strip()] = v.strip().strip("\"'")
    return out


def own_targets_csv(responder_ip: str) -> str:
    """Our own endpoints as a Citizen-Lab-shaped CSV.

    One row per responder port, dialled by address, so Tier 1 records
    continuously whether our own address answers on each port. Blocking of
    the address itself shows up here before any experiment is run.
    """
    rows = ["url,category_code,category_description,date_added,source,notes"]
    for port in RESPONDER_TLS_PORTS:
        suffix = "" if port == "443" else f":{port}"
        rows.append(f"https://{responder_ip}{suffix}/v1/health,OWN-RESPONDER,Own responder port {port},2026-09-04,rnfo,")
    return "\n".join(rows) + "\n"

FILES = [
    "rnfo-probe@.service",
    "rnfo-probe-full.timer",
    "rnfo-probe-controls.timer",
    "install.sh",
]


def build(arch: str) -> bytes:
    out = os.path.join(REPO, "bin", f"rnfo-probe-linux-{arch}")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
    # -trimpath and reproducible flags: the binary that produced a dataset
    # should be rebuildable byte-for-byte from the tagged commit.
    cmd = ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./probe"]
    print(f"==> building {arch}: {' '.join(cmd)}")
    subprocess.run(cmd, cwd=REPO, env=env, check=True)
    with open(out, "rb") as f:
        return f.read()


def connect(host: str, password: str | None, jump_sock=None) -> paramiko.SSHClient:
    user, _, hostname = host.rpartition("@")
    user = user or "root"
    c = paramiko.SSHClient()
    c.load_system_host_keys()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    attempts = [dict(key_filename=os.path.expanduser("~/.ssh/id_ed25519"),
                     look_for_keys=True, allow_agent=True)]
    if password:
        attempts.append(dict(password=password, look_for_keys=False, allow_agent=False))
    last = None
    for kw in attempts:
        try:
            c.connect(hostname, username=user, timeout=20, banner_timeout=25,
                      auth_timeout=25, sock=jump_sock, **kw)
            return c
        except Exception as e:  # noqa: BLE001
            last = e
    raise SystemExit(f"cannot connect to {host}: {last}")


def run(c: paramiko.SSHClient, cmd: str, check: bool = True) -> str:
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


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("host")
    ap.add_argument("probe_id")
    ap.add_argument("net", choices=["hosting", "eyeball", "mobile"])
    ap.add_argument("--arch", default="amd64", choices=["amd64", "arm64"])
    ap.add_argument("--password-env", default="RNFO_DEPLOY_PASSWORD")
    ap.add_argument("--jump", default=None)
    ap.add_argument("--run", action="store_true", help="run a controls measurement immediately")
    ap.add_argument("--pubkey", default=os.path.expanduser("~/.ssh/id_ed25519.pub"))
    a = ap.parse_args()

    binary = build(a.arch)

    # Bundle everything into one tar so the upload is a single stream and a
    # half-finished deployment cannot leave a probe running a mixed version.
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        info = tarfile.TarInfo("rnfo-probe")
        info.size, info.mode = len(binary), 0o755
        tar.addfile(info, io.BytesIO(binary))
        for name in FILES:
            path = os.path.join(REPO, "probe", "deploy", name)
            with open(path, "rb") as f:
                data = f.read().replace(b"\r\n", b"\n")  # CRLF from a Windows checkout would break the shebang
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = 0o755 if name.endswith(".sh") else 0o644
            tar.addfile(info, io.BytesIO(data))
    payload = buf.getvalue()

    jump_sock = None
    if a.jump:
        j = connect(a.jump, os.environ.get("RNFO_JUMP_PASSWORD"))
        host_only = a.host.rpartition("@")[2]
        jump_sock = j.get_transport().open_channel("direct-tcpip", (host_only, 22), ("127.0.0.1", 0))

    c = connect(a.host, os.environ.get(a.password_env), jump_sock)
    print(f"==> connected to {a.host}")

    remote = "/tmp/rnfo-deploy"
    run(c, f"rm -rf {remote} && mkdir -p {remote}")
    sftp = c.open_sftp()
    with sftp.file(posixpath.join(remote, "bundle.tgz"), "wb") as f:
        f.write(payload)
    sftp.close()
    run(c, f"cd {remote} && tar xzf bundle.tgz && rm bundle.tgz")

    # Key-based access, so the password can be retired once this succeeds.
    if os.path.exists(a.pubkey):
        with open(a.pubkey) as f:
            key = f.read().strip()
        run(c, "install -d -m 700 /root/.ssh && touch /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys")
        run(c, f"grep -qF '{key}' /root/.ssh/authorized_keys || echo '{key}' >> /root/.ssh/authorized_keys")
        print("==> ssh key present")

    env = load_env()
    if env.get("RNFO_RESPONDER_IP"):
        csv = own_targets_csv(env["RNFO_RESPONDER_IP"])
        run(c, "install -d -m 0755 /etc/rnfo")
        sftp = c.open_sftp()
        with sftp.file("/etc/rnfo/own.csv", "w") as f:
            f.write(csv)
        sftp.close()
        run(c, "chmod 0644 /etc/rnfo/own.csv")
        print(f"==> own targets: responder {env['RNFO_RESPONDER_IP']} on {len(RESPONDER_TLS_PORTS)} ports")

    run(c, f"bash {remote}/install.sh {a.probe_id} {a.net}")
    run(c, f"rm -rf {remote}")

    if a.run:
        print("==> running a controls measurement now")
        run(c, "systemctl start rnfo-probe@controls.service", check=False)
        time.sleep(3)
        run(c, "systemctl status rnfo-probe@controls.service --no-pager -n 20", check=False)

    c.close()
    print("==> done")


if __name__ == "__main__":
    main()
