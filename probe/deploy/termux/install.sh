#!/data/data/com.termux/files/usr/bin/bash
# Install the RNFO agent on an Android handset under Termux.
#
# Runs on the phone, as the Termux user, no root. Everything lives under
# $HOME/rnfo. There is no systemd, so the agent runs in -daemon mode and keeps
# its own slot-aligned schedule; Termux:Boot starts it after a reboot.
#
# Usage: install.sh <probe-id> <eyeball|mobile> <dns host[:port]> <max-body-bytes> [tunnel user@host] [tunnel port]
set -euo pipefail

PROBE_ID="${1:?usage: install.sh <probe-id> <eyeball|mobile> <dns> <max-body>}"
NET_TYPE="${2:?}"
DNS="${3:?dns resolver is required: Android has no /etc/resolv.conf}"
MAX_BODY="${4:-2097152}"
TUNNEL_HOST="${5:-}"
TUNNEL_PORT="${6:-}"

case "$NET_TYPE" in
  eyeball|mobile) ;;
  *) echo "net type on a handset must be eyeball or mobile" >&2; exit 2 ;;
esac

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE="$HOME/rnfo"

echo "==> packages (termux-api for the wake lock, iproute2 for the gateway, openssh for access)"
pkg install -y termux-api iproute2 openssh >/dev/null 2>&1 || pkg install -y termux-api iproute2 openssh

echo "==> directories"
mkdir -p "$BASE/bin" "$BASE/data/measurements" "$BASE/data/runs" "$BASE/state" "$HOME/.termux/boot"

echo "==> binary"
install -m 0755 "$SRC/rnfo-probe" "$BASE/bin/rnfo-probe.new"
mv -f "$BASE/bin/rnfo-probe.new" "$BASE/bin/rnfo-probe"
"$BASE/bin/rnfo-probe" -version

echo "==> configuration"
[ -f "$BASE/probe.env" ] && cp -a "$BASE/probe.env" "$BASE/probe.env.bak-$(date -u +%Y%m%d%H%M%S)"
cat > "$BASE/probe.env" <<EOF
# RNFO probe configuration (Termux). Only this file differs between probes.
RNFO_PROBE_ID=$PROBE_ID
RNFO_NET=$NET_TYPE
RNFO_DATA_DIR=$BASE/data
RNFO_STATE_DIR=$BASE/state
RNFO_OWN_TARGETS=$BASE/own.csv
RNFO_IP_FAMILY=v4
RNFO_CONCURRENCY=8
RNFO_KEEP_DAYS=90
# Android has no /etc/resolv.conf; a static binary would resolve nothing.
# The home router keeps the ISP's resolver in the path. Recorded per run.
RNFO_DNS=$DNS
# Body cap per target. Lowered on a metered SIM. Recorded per run.
RNFO_MAX_BODY=$MAX_BODY
# Go verifies TLS against the system CA store, and Termux has no /etc/ssl.
# Without this the identity lookup fails and the probe cannot name its ASN.
# Measurements are unaffected (they fingerprint certificates, not trust them).
SSL_CERT_FILE=$PREFIX/etc/tls/cert.pem
# Reverse tunnel to the collector's jump host, so a phone behind NAT can be
# pulled from. Forwarding-only key; the phone can bind one loopback port there
# and nothing else.
RNFO_TUNNEL_HOST=$TUNNEL_HOST
RNFO_TUNNEL_PORT=$TUNNEL_PORT
RNFO_TUNNEL_KEY=$HOME/.ssh/rnfo_tunnel
EOF
[ -f "$PREFIX/etc/tls/cert.pem" ] || pkg install -y ca-certificates >/dev/null 2>&1 || true

echo "==> tunnel key"
mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
if [ ! -f "$HOME/.ssh/rnfo_tunnel" ]; then
  ssh-keygen -q -t ed25519 -N "" -C "rnfo-tunnel@$PROBE_ID" -f "$HOME/.ssh/rnfo_tunnel"
fi
echo "    public key (authorize it on the jump host with restrict,port-forwarding,permitlisten):"
echo "    $(cat "$HOME/.ssh/rnfo_tunnel.pub")"
chmod 0600 "$BASE/probe.env"

echo "==> supervisor and boot script (Termux:Boot runs everything in ~/.termux/boot after unlock)"
install -m 0755 "$SRC/supervise.sh" "$BASE/bin/supervise.sh"
install -m 0755 "$SRC/rnfo-boot.sh" "$HOME/.termux/boot/rnfo.sh"

echo "==> recovery hook: opening the Termux app starts everything"
# Termux sources ~/.bashrc for every interactive shell. If the OS ever blocks
# Termux:Boot, opening the app by hand becomes a full recovery. Idempotent.
grep -qF '.termux/boot/rnfo.sh' "$HOME/.bashrc" 2>/dev/null ||   printf '
# RNFO: (re)start the probe and its tunnel; idempotent
[ -x "$HOME/.termux/boot/rnfo.sh" ] && "$HOME/.termux/boot/rnfo.sh"
' >> "$HOME/.bashrc"

echo "==> ssh access (key from the collector)"
mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
if [ -f "$SRC/authorized_key" ]; then
  touch "$HOME/.ssh/authorized_keys"
  grep -qF "$(cat "$SRC/authorized_key")" "$HOME/.ssh/authorized_keys" || cat "$SRC/authorized_key" >> "$HOME/.ssh/authorized_keys"
  chmod 600 "$HOME/.ssh/authorized_keys"
fi

echo "==> starting now"
"$HOME/.termux/boot/rnfo.sh"
sleep 3
if pgrep -f "rnfo-probe -daemo[n]" >/dev/null; then
  echo "    daemon running (pid $(pgrep -f 'rnfo-probe -daemo[n]' | head -1))"
else
  echo "    daemon did not start; see $BASE/daemon.log" >&2
  tail -20 "$BASE/daemon.log" >&2 || true
  exit 1
fi

echo
echo "Probe $PROBE_ID ($NET_TYPE) installed under $BASE."
echo "Still to do BY HAND on the phone, or the OS will kill it within hours:"
echo "  Settings -> Apps -> Termux        -> Battery -> Unrestricted / no optimisation"
echo "  Settings -> Apps -> Termux:Boot   -> Battery -> Unrestricted; open the app once"
echo "  Settings -> Apps -> Termux:API    -> Battery -> Unrestricted"
echo "  Keep the phone plugged in. Wi-Fi: 'keep on during sleep'. Do NOT install a VPN app on it."
