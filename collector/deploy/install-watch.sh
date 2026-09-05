#!/usr/bin/env bash
# Install the health watcher on an always-on host.
#
# The watcher runs `rnfo-collect health -alert` hourly against the probes it
# can reach and sends a message to RNFO_ALERT_URL when one goes stale or
# recovers. It is installed on more than one host because no single machine
# can see every probe: the Frankfurt prefix does not exchange TCP with Russia.
#
# Expects, next to this script: rnfo-collect (binary), probes.yaml, watch.env
# (RNFO_WATCH_ARGS), dot.env (the collector's .env for this host), and
# optionally ssh_config.snippet (aliases the .env refers to, appended to
# /root/.ssh/config once).
set -euo pipefail
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
W=/opt/rnfo/watch

echo "==> files"
install -d -m 0755 /opt/rnfo/bin
install -d -m 0700 "$W"
install -m 0755 "$SRC/rnfo-collect" /opt/rnfo/bin/rnfo-collect.new
mv -f /opt/rnfo/bin/rnfo-collect.new /opt/rnfo/bin/rnfo-collect
install -m 0644 "$SRC/probes.yaml" "$W/probes.yaml"
install -m 0600 "$SRC/watch.env" "$W/watch.env"
install -m 0600 "$SRC/dot.env" "$W/.env"

if [ -f "$SRC/ssh_config.snippet" ]; then
  echo "==> ssh aliases"
  install -d -m 0700 /root/.ssh
  touch /root/.ssh/config && chmod 0600 /root/.ssh/config
  MARK="$(head -1 "$SRC/ssh_config.snippet")"
  grep -qF "$MARK" /root/.ssh/config || cat "$SRC/ssh_config.snippet" >> /root/.ssh/config
fi

echo "==> units"
install -m 0644 "$SRC/rnfo-watch.service" /etc/systemd/system/
install -m 0644 "$SRC/rnfo-watch.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now rnfo-watch.timer

echo "==> first run"
systemctl start rnfo-watch.service || true
journalctl -u rnfo-watch.service -n 8 --no-pager -o cat
systemctl list-timers rnfo-watch.timer --no-pager --no-legend
