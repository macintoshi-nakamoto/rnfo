#!/usr/bin/env bash
# Install or update the RNFO measurement agent on a probe.
#
# Every probe runs this identical script. The only thing that differs between
# deployments is /etc/rnfo/probe.env, which carries the probe id and network
# type. That is a project requirement: two deployments that
# differ in anything else are two different instruments, and the data cannot be
# compared across them.
#
# Idempotent: safe to re-run for an upgrade.
#
# Usage: install.sh <probe-id> <hosting|eyeball|mobile>
set -euo pipefail

PROBE_ID="${1:?usage: install.sh <probe-id> <hosting|eyeball|mobile>}"
NET_TYPE="${2:?usage: install.sh <probe-id> <hosting|eyeball|mobile>}"

case "$NET_TYPE" in
  hosting|eyeball|mobile) ;;
  *) echo "net type must be hosting, eyeball or mobile" >&2; exit 2 ;;
esac

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFIX=/opt/rnfo
STATE=/var/lib/rnfo

echo "==> user and directories"
if ! id -u rnfo >/dev/null 2>&1; then
  useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin rnfo
fi
install -d -m 0755 "$PREFIX/bin"
install -d -m 0750 -o rnfo -g rnfo "$STATE" "$STATE/data" "$STATE/data/measurements" "$STATE/data/runs" "$STATE/state"
install -d -m 0755 /etc/rnfo

echo "==> binary"
# Written to a temporary name and renamed. A plain install over a binary that
# is currently executing fails with ETXTBSY, which would abort an upgrade in
# the middle of a measurement run; rename is atomic and the running process
# keeps its own inode until it exits.
install -m 0755 "$SRC/rnfo-probe" "$PREFIX/bin/rnfo-probe.new"
mv -f "$PREFIX/bin/rnfo-probe.new" "$PREFIX/bin/rnfo-probe"

echo "==> status script (the one command a watcher's key may run)"
install -m 0755 "$SRC/rnfo-status" "$PREFIX/bin/rnfo-status"
if [ -n "${WATCH_PUBKEY:-}" ]; then
  install -d -m 700 /root/.ssh && touch /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys
  LINE="restrict,command=\"$PREFIX/bin/rnfo-status\" $WATCH_PUBKEY"
  grep -qF "$WATCH_PUBKEY" /root/.ssh/authorized_keys || echo "$LINE" >> /root/.ssh/authorized_keys
  echo "    watcher key authorised for rnfo-status only"
fi

echo "==> configuration"
# Written every time so that a probe's identity can be corrected by re-running
# the installer, but never silently: the previous file is kept.
if [ -f /etc/rnfo/probe.env ]; then
  cp -a /etc/rnfo/probe.env "/etc/rnfo/probe.env.bak-$(date -u +%Y%m%d%H%M%S)"
fi
cat > /etc/rnfo/probe.env <<EOF
# RNFO probe configuration. This file, and only this file, differs between
# probes. Do not put secrets here: probes hold no credentials by design, and
# collection is pull-based.
RNFO_PROBE_ID=$PROBE_ID
RNFO_NET=$NET_TYPE
RNFO_DATA_DIR=$STATE/data
RNFO_STATE_DIR=$STATE/state
# Address family is fixed across the study so that every probe measures the
# same thing. A deliberate v4-versus-v6 comparison is a separate run, not a
# silent difference between machines.
RNFO_IP_FAMILY=v4
RNFO_CONCURRENCY=12
# Our own endpoints (the responder). Written by tools/deploy.py from .env; the
# agent tolerates the file being absent.
RNFO_OWN_TARGETS=/etc/rnfo/own.csv
RNFO_KEEP_DAYS=90
EOF
chmod 0644 /etc/rnfo/probe.env

echo "==> clock"
# Two-sided packet captures in Tier 2 can only be correlated if the clocks
# agree to within a few milliseconds. chrony is preferred; timesyncd is
# accepted for Tier 1 but flagged, because it is SNTP and drifts further.
if command -v chronyc >/dev/null 2>&1; then
  systemctl is-active --quiet chrony 2>/dev/null || systemctl is-active --quiet chronyd 2>/dev/null || true
  chronyc tracking 2>/dev/null | sed -n '1p;5p' || true
else
  echo "    chrony is NOT installed; falling back to $(systemctl is-active systemd-timesyncd 2>/dev/null || echo none)"
  echo "    Tier 2 will need chrony here. Install it deliberately, not from this script."
fi

echo "==> systemd units"
install -m 0644 "$SRC/rnfo-probe@.service" /etc/systemd/system/
install -m 0644 "$SRC/rnfo-probe-full.timer" /etc/systemd/system/
install -m 0644 "$SRC/rnfo-probe-controls.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now rnfo-probe-full.timer rnfo-probe-controls.timer

echo "==> installed"
"$PREFIX/bin/rnfo-probe" -version
systemctl list-timers 'rnfo-*' --no-pager --no-legend || true
echo
echo "Probe $PROBE_ID ($NET_TYPE) is scheduled."
echo "First full run: next 00/06/12/18 UTC. Controls: next quarter hour."
echo "Run one now with:  systemctl start rnfo-probe@controls.service"
