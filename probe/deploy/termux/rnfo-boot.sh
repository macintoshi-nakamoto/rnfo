#!/data/data/com.termux/files/usr/bin/bash
# Started by Termux:Boot after every reboot (once the device is unlocked), and
# by install.sh. Idempotent: safe to run again while things are up.
#
# Keeps three things alive: a wake lock so Android does not doze the process,
# sshd so the collector can reach the phone, and the agent in daemon mode,
# restarted in a loop if it ever exits.
BASE="$HOME/rnfo"
mkdir -p "$BASE"
if [ -f "$BASE/probe.env" ]; then set -a; . "$BASE/probe.env"; set +a; fi

# Wake lock: without it the CPU sleeps and the slot boundaries drift or are
# missed entirely. Needs the Termux:API app installed.
termux-wake-lock 2>/dev/null || true

# sshd on 8022. Already-running is fine.
pgrep -x sshd >/dev/null || sshd

# Reverse tunnel to the jump host, if configured. -N: no command; the account
# on the far side is forwarding-only anyway. ExitOnForwardFailure makes a
# stale listener on the far side a retry rather than a silent half-tunnel.
if [ -n "${RNFO_TUNNEL_HOST:-}" ] && [ -n "${RNFO_TUNNEL_PORT:-}" ]; then
  if ! pgrep -f "ssh -N .*:${RNFO_TUNNEL_PORT}:127.0.0.1:8022" >/dev/null; then
    nohup bash -c '
      while true; do
        ssh -N \
          -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
          -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes \
          -i "$RNFO_TUNNEL_KEY" \
          -R "127.0.0.1:${RNFO_TUNNEL_PORT}:127.0.0.1:8022" "$RNFO_TUNNEL_HOST" >> "$HOME/rnfo/tunnel.log" 2>&1
        echo "$(date -u +%FT%TZ) tunnel exited rc=$?, retrying in 20s" >> "$HOME/rnfo/tunnel.log"
        sleep 20
      done
    ' >/dev/null 2>&1 &
  fi
fi

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
