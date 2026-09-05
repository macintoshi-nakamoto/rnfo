#!/data/data/com.termux/files/usr/bin/sh
# Prints this probe's newest controls run record, one JSON line. Same body as
# probe/deploy/rnfo-status; only the interpreter path differs, because Termux
# has no /bin/sh. A watcher's key is authorised to run exactly this.
D=$(for f in /etc/rnfo/probe.env "$HOME/rnfo/probe.env"; do [ -f "$f" ] && grep -m1 "^RNFO_DATA_DIR=" "$f"; done | head -1 | cut -d= -f2)
[ -z "$D" ] && D=/var/lib/rnfo/data
Y=$(date -u -d yesterday +%F 2>/dev/null || date -u -v-1d +%F 2>/dev/null)
T=$(date -u +%F)
for f in "$D/runs/$Y.jsonl" "$D/runs/$T.jsonl"; do
  [ -f "$f" ] && grep '"profile":"controls"' "$f"
done | grep '"kind":"run"' | tail -1
