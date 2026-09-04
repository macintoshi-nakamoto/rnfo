#!/usr/bin/env bash
# Re-pin the Citizen Lab test lists.
#
# This is an event, not a routine. The lists are compiled into the agent, so a
# refresh means rebuilding and redeploying every probe at once. Probes measuring
# different target sets produce data that looks like a change in the network and
# is not. Record the refresh in docs/CHANGELOG-lists.md.
set -euo pipefail
cd "$(dirname "$0")/.."
BASE=https://raw.githubusercontent.com/citizenlab/test-lists/master/lists
for l in global ru; do
  curl -fsSL "$BASE/$l.csv" -o "probe/lists/citizenlab-$l.csv"
done
SHA=$(curl -fsS "https://api.github.com/repos/citizenlab/test-lists/commits?path=lists/global.csv&per_page=1" \
      | grep -m1 '"sha"' | sed 's/.*"sha": *"\([^"]*\)".*/\1/')
G=$(sha256sum probe/lists/citizenlab-global.csv | cut -d' ' -f1)
R=$(sha256sum probe/lists/citizenlab-ru.csv | cut -d' ' -f1)
cat > probe/lists/MANIFEST.json <<JSON
{
  "fetched_utc": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source_repo": "https://github.com/citizenlab/test-lists",
  "source_commit": "$SHA",
  "files": {
    "citizenlab-global.csv": { "sha256": "$G", "urls": $(($(wc -l < probe/lists/citizenlab-global.csv) - 1)) },
    "citizenlab-ru.csv":     { "sha256": "$R", "urls": $(($(wc -l < probe/lists/citizenlab-ru.csv) - 1)) }
  },
  "note": "Pinned copy. Refreshing is an event: rebuild, redeploy every probe together, and record it in docs/CHANGELOG-lists.md."
}
JSON
echo "pinned at $SHA"
git diff --stat probe/lists/ || true
