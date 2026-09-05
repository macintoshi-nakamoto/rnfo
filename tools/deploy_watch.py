#!/usr/bin/env python3
"""Install the health watcher on an always-on host.

    python tools/deploy_watch.py root@<host> \
        --probes ru-msk-vps,ru-mow-home,nl-lim-panel \
        --map ru-msk-vps=local \
        --map ru-mow-home=rnfo-home \
        --map nl-lim-panel=root@<panel-address> \
        --alias "rnfo-home=127.0.0.1:2201:termux" \
        --key /root/.ssh/rnfo_watch

--map says how the watcher reaches each probe ("local" = itself). --alias adds
an ssh Host entry on the watcher for names used in --map. The alert URL is
copied from the workstation's .env. Everything secret lands in root-only files
under /opt/rnfo/watch on the host; nothing is written into the repository.
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
from deploy import connect, load_env, run  # noqa: E402


def build() -> bytes:
    out = os.path.join(REPO, "bin", "rnfo-collect-linux-amd64")
    env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
    print("==> building collector linux/amd64")
    subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./collector"], cwd=REPO, env=env, check=True)
    with open(out, "rb") as f:
        return f.read()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("host")
    ap.add_argument("--probes", required=True, help="comma-separated probe ids this watcher checks")
    ap.add_argument("--map", action="append", default=[], help="probe=hostspec; hostspec is 'local', user@host, or an ssh alias")
    ap.add_argument("--alias", action="append", default=[], help="name=hostname:port:user, written to /root/.ssh/config")
    ap.add_argument("--key", default="/root/.ssh/rnfo_watch", help="private key on the watcher for reaching probes")
    ap.add_argument("--responder", action="store_true", help="also check the responder (only where it is reachable)")
    ap.add_argument("--password-env", default="RNFO_DEPLOY_PASSWORD")
    a = ap.parse_args()
    if not a.key.startswith("/"):
        # Git Bash rewrites absolute Unix paths in arguments into Windows
        # paths ("/root/x" becomes "C:/Program Files/Git/root/x"), and the
        # result then lands in the watcher's .env on a Linux host. Refuse.
        raise SystemExit(f"--key must be an absolute path on the watcher, got {a.key!r}. "
                         "From Git Bash run with MSYS_NO_PATHCONV=1, or omit --key for the default.")

    env = load_env()
    alert = env.get("RNFO_ALERT_URL", "")
    if not alert:
        print("warning: RNFO_ALERT_URL not in .env; the watcher will check but never alert", file=sys.stderr)

    dot_env = [f"RNFO_SSH_KEY={a.key}"]
    for m in a.map:
        probe, spec = m.split("=", 1)
        dot_env.append(f"RNFO_SSH_{probe.replace('-', '_')}={spec}")
    if alert:
        dot_env.append(f"RNFO_ALERT_URL={alert}")
    if a.responder and env.get("RNFO_RESPONDER_IP"):
        dot_env.append(f"RNFO_RESPONDER_IP={env['RNFO_RESPONDER_IP']}")
    args = f"-probes {a.probes}" + (" -responder" if a.responder else "")

    snippet = ""
    if a.alias:
        lines = ["# RNFO watcher aliases (managed by tools/deploy_watch.py)"]
        for al in a.alias:
            name, spec = al.split("=", 1)
            hostname, port, user = spec.split(":")
            lines += [f"Host {name}", f"    HostName {hostname}", f"    Port {port}", f"    User {user}",
                      f"    IdentityFile {a.key}", "    StrictHostKeyChecking accept-new", "    BatchMode yes", ""]
        snippet = "\n".join(lines) + "\n"

    binary = build()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        def add(name, data, mode=0o644):
            info = tarfile.TarInfo(name)
            info.size, info.mode = len(data), mode
            tar.addfile(info, io.BytesIO(data))
        add("rnfo-collect", binary, 0o755)
        with open(os.path.join(REPO, "probes.yaml"), "rb") as f:
            add("probes.yaml", f.read())
        for name in ("rnfo-watch.service", "rnfo-watch.timer", "install-watch.sh"):
            with open(os.path.join(REPO, "collector", "deploy", name), "rb") as f:
                add(name, f.read().replace(b"\r\n", b"\n"), 0o755 if name.endswith(".sh") else 0o644)
        add("watch.env", f"RNFO_WATCH_ARGS={args}\n".encode(), 0o600)
        add("dot.env", ("\n".join(dot_env) + "\n").encode(), 0o600)
        if snippet:
            add("ssh_config.snippet", snippet.encode())

    c = connect(a.host, os.environ.get(a.password_env))
    print(f"==> connected to {a.host}")
    remote = "/tmp/rnfo-watch-deploy"
    run(c, f"rm -rf {remote} && mkdir -p {remote}")
    sftp = c.open_sftp()
    with sftp.file(posixpath.join(remote, "b.tgz"), "wb") as f:
        f.write(buf.getvalue())
    sftp.close()
    run(c, f"cd {remote} && tar xzf b.tgz && rm b.tgz && bash {remote}/install-watch.sh && rm -rf {remote}")
    c.close()
    print("==> done")


if __name__ == "__main__":
    main()
