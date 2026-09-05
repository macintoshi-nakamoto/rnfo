#!/data/data/com.termux/files/usr/bin/bash
# Keeps the agent running. A separate file on purpose: this process's command
# line is "bash .../supervise.sh" and does not contain "rnfo-probe -daemon",
# so an operator restarting the daemon with pkill restarts it instead of
# taking the supervisor down with it. Learned the hard way on 2026-09-05.
#
# Logs are rotated here because nothing else on a phone will do it: journald
# does not exist and a year of restarts must not fill the storage.
rotate() {
  f="$1"
  if [ -f "$f" ] && [ "$(stat -c %s "$f" 2>/dev/null || echo 0)" -gt 5242880 ]; then
    mv -f "$f" "$f.1"
  fi
}
while true; do
  rotate "$HOME/rnfo/daemon.log"
  rotate "$HOME/rnfo/tunnel.log"
  set -a; . "$HOME/rnfo/probe.env"; set +a
  "$HOME/rnfo/bin/rnfo-probe" -daemon >> "$HOME/rnfo/daemon.log" 2>&1
  echo "$(date -u +%FT%TZ) daemon exited rc=$?, restarting in 30s" >> "$HOME/rnfo/daemon.log"
  sleep 30
done
