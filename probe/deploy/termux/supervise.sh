#!/data/data/com.termux/files/usr/bin/bash
# Keeps the agent running. A separate file on purpose: this process's command
# line is "bash .../supervise.sh" and does not contain "rnfo-probe -daemon",
# so an operator restarting the daemon with pkill restarts it instead of
# taking the supervisor down with it. Learned the hard way on 2026-09-05.
while true; do
  set -a; . "$HOME/rnfo/probe.env"; set +a
  "$HOME/rnfo/bin/rnfo-probe" -daemon >> "$HOME/rnfo/daemon.log" 2>&1
  echo "$(date -u +%FT%TZ) daemon exited rc=$?, restarting in 30s" >> "$HOME/rnfo/daemon.log"
  sleep 30
done
