#!/data/data/com.termux/files/usr/bin/bash
# Started by Termux:Boot after every reboot (once the device is unlocked), and
# by install.sh. Idempotent: safe to run again while things are up.
#
# Keeps three things alive: a wake lock so Android does not doze the process,
# sshd so the collector can reach the phone, and the agent in daemon mode,
# restarted in a loop if it ever exits.
BASE="$HOME/rnfo"
mkdir -p "$BASE"

# Wake lock: without it the CPU sleeps and the slot boundaries drift or are
# missed entirely. Needs the Termux:API app installed.
termux-wake-lock 2>/dev/null || true

# sshd on 8022. Already-running is fine.
pgrep -x sshd >/dev/null || sshd

# The agent. One supervisor loop; the agent itself decides when to measure.
if ! pgrep -f "rnfo-probe -daemon" >/dev/null; then
  nohup bash -c '
    while true; do
      set -a; . "$HOME/rnfo/probe.env"; set +a
      "$HOME/rnfo/bin/rnfo-probe" -daemon >> "$HOME/rnfo/daemon.log" 2>&1
      echo "$(date -u +%FT%TZ) daemon exited rc=$?, restarting in 30s" >> "$HOME/rnfo/daemon.log"
      sleep 30
    done
  ' >/dev/null 2>&1 &
fi
