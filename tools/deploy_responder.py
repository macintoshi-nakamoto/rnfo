#!/usr/bin/env python3
"""Build and install the responder on a dedicated foreign host.

Deliberately a separate tool from deploy.py. Installing the responder is not
routine: it opens listening ports, and it belongs only on a machine that runs
nothing else. Keeping it out of the probe installer means it cannot be put on a
production host by reflex.

Usage:
    python tools/deploy_responder.py <host> [--tls-ports 443,8443,2053,9443]
                                            [--raw-ports 8080]
                                            [--names rnfo-responder.invalid]

The token is read from RNFO_RESPONDER_TOKEN, or generated and printed once so
it can be stored in .env.
"""
import argparse
import io
import os
import posixpath
import secrets
import subprocess
import sys
import tarfile

import paramiko

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = ["rnfo-responder.service", "install.sh"]


def build(arch: str) -> bytes:
    out = os.path.join(REPO, "bin", f"rnfo-responder-linux-{arch}")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
    cmd = ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./responder"]
    print(f"==> building {arch}")
    subprocess.run(cmd, cwd=REPO, env=env, check=True)
    with open(out, "rb") as f:
        return f.read()


def connect(host: str, password: str | None) -> paramiko.SSHClient:
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
            c.connect(hostname, username=user, timeout=20, banner_timeout=25, auth_timeout=25, **kw)
            return c
        except Exception as e:  # noqa: BLE001
            last = e
    raise SystemExit(f"cannot connect to {host}: {last}")


def run(c, cmd, check=True, env=None):
    if env:
        prefix = " ".join(f"{k}={v}" for k, v in env.items())
        cmd = f"{prefix} {cmd}"
    _, so, se = c.exec_command(cmd, timeout=300)
    out = so.read().decode("utf-8", "replace")
    err = se.read().decode("utf-8", "replace")
    rc = so.channel.recv_exit_status()
    if out.strip():
        print(out.rstrip())
    if err.strip():
        print(err.rstrip(), file=sys.stderr)
    if check and rc != 0:
        raise SystemExit(f"remote command failed (rc={rc})")
    return out


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("host")
    ap.add_argument("--tls-ports", default="443,8443,2053,9443")
    ap.add_argument("--raw-ports", default="8080")
    ap.add_argument("--names", default="rnfo-responder.invalid")
    ap.add_argument("--arch", default="amd64", choices=["amd64", "arm64"])
    ap.add_argument("--password-env", default="RNFO_DEPLOY_PASSWORD")
    a = ap.parse_args()

    token = os.environ.get("RNFO_RESPONDER_TOKEN")
    generated = False
    if not token:
        token = secrets.token_urlsafe(32)
        generated = True

    binary = build(a.arch)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        info = tarfile.TarInfo("rnfo-responder")
        info.size, info.mode = len(binary), 0o755
        tar.addfile(info, io.BytesIO(binary))
        for name in FILES:
            with open(os.path.join(REPO, "responder", "deploy", name), "rb") as f:
                data = f.read().replace(b"\r\n", b"\n")
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = 0o755 if name.endswith(".sh") else 0o644
            tar.addfile(info, io.BytesIO(data))

    c = connect(a.host, os.environ.get(a.password_env))
    print(f"==> connected to {a.host}")
    remote = "/tmp/rnfo-responder-deploy"
    run(c, f"rm -rf {remote} && mkdir -p {remote}")
    sftp = c.open_sftp()
    with sftp.file(posixpath.join(remote, "b.tgz"), "wb") as f:
        f.write(buf.getvalue())
    sftp.close()
    run(c, f"cd {remote} && tar xzf b.tgz && rm b.tgz")
    run(c, f"bash {remote}/install.sh '{a.tls_ports}' '{a.raw_ports}' '{a.names}'",
        env={"RNFO_RESPONDER_TOKEN": token})
    run(c, f"rm -rf {remote}")
    c.close()

    if generated:
        print("\n==> a token was generated. Put this line in .env (git-ignored):")
        print(f"RNFO_RESPONDER_TOKEN={token}")
    print("==> done")


if __name__ == "__main__":
    main()
