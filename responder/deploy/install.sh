#!/usr/bin/env bash
# Install the RNFO responder: the endpoint probes measure against.
#
# This must only ever run on a machine that carries nothing else. The responder
# exists to answer questions of the form "same address, different name" and
# "exactly how many bytes get through". Both answers are worthless on a host
# whose behaviour is already determined by other traffic it carries.
#
# Idempotent. Usage: install.sh <tls-ports> <raw-ports> <cert-names>
set -euo pipefail

TLS_PORTS="${1:-443,8443,2053,9443}"
RAW_PORTS="${2:-8080}"
CERT_NAMES="${3:-rnfo-responder.invalid}"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFIX=/opt/rnfo
STATE=/var/lib/rnfo-responder

if [ -z "${RNFO_RESPONDER_TOKEN:-}" ]; then
  echo "RNFO_RESPONDER_TOKEN must be set in the environment" >&2
  exit 2
fi

echo "==> refusing to run on a shared machine"
# A crude but effective guard. If something is already serving on the ports we
# are about to take, this is not a dedicated host and the deployment is wrong.
for p in ${TLS_PORTS//,/ }; do
  if ss -lnt "sport = :$p" 2>/dev/null | grep -q LISTEN; then
    echo "    port $p is already in use; this host is not dedicated. Aborting." >&2
    exit 3
  fi
done

echo "==> user and directories"
if ! id -u rnfo-responder >/dev/null 2>&1; then
  useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin rnfo-responder
fi
install -d -m 0755 "$PREFIX/bin"
install -d -m 0750 -o rnfo-responder -g rnfo-responder "$STATE"

echo "==> binary"
install -m 0755 "$SRC/rnfo-responder" "$PREFIX/bin/rnfo-responder.new"
mv -f "$PREFIX/bin/rnfo-responder.new" "$PREFIX/bin/rnfo-responder"

echo "==> configuration"
install -d -m 0755 /etc/rnfo
umask 077
cat > /etc/rnfo/responder.env <<EOF
# The token decides who gets logged in detail. Everyone else is a counter and
# nothing more, so that this service never records a stranger's connection.
RNFO_RESPONDER_TOKEN=$RNFO_RESPONDER_TOKEN
RNFO_PORTS=$TLS_PORTS
RNFO_RAW_PORTS=$RAW_PORTS
RNFO_CERT_NAMES=$CERT_NAMES
RNFO_CERT_DIR=$STATE
RNFO_RESPONDER_LOG=$STATE/connections.jsonl
EOF
chmod 0640 /etc/rnfo/responder.env
chown root:rnfo-responder /etc/rnfo/responder.env
umask 022

echo "==> firewall"
# The set of open ports is part of the experiment: a question like "is this
# port treated differently" is only meaningful if we know exactly which ports
# answer. So the firewall is explicit rather than absent.
if command -v ufw >/dev/null 2>&1; then
  ufw allow 22/tcp comment 'SSH' >/dev/null
  for p in ${TLS_PORTS//,/ }; do ufw allow "$p/tcp" comment 'RNFO responder TLS' >/dev/null; done
  for p in ${RAW_PORTS//,/ }; do ufw allow "$p/tcp" comment 'RNFO responder raw' >/dev/null; done
  ufw --force enable >/dev/null
  ufw status | sed 's/^/    /'
fi

echo "==> systemd"
install -m 0644 "$SRC/rnfo-responder.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now rnfo-responder.service
sleep 2
systemctl is-active rnfo-responder.service

echo "==> certificate fingerprint (record this in probes.yaml)"
journalctl -u rnfo-responder.service -n 20 --no-pager | grep -o 'sha256=[0-9a-f]*' | tail -1

echo
echo "Responder is up on TLS $TLS_PORTS and raw $RAW_PORTS."
