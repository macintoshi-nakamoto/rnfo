# Operations

Everything needed to run, verify and extend the measurement network. Written so that
the answer to "is it still collecting?" takes one command, because by 2026-12-01 the
milestone is *zero manual intervention* and anything harder than one command will not
get done during exam season.

---

## Daily reality check

```bash
ssh root@<probe> 'systemctl list-timers "rnfo-*" --no-pager; tail -2 /var/lib/rnfo/data/runs/$(date -u +%F).jsonl | jq -c "{run_id,rows,ok,failed,healthy,duration_s}"'
```

`healthy: false` means the probe lost its network during that run, not that the
network filtered everything. Those runs are excluded from analysis.

---

## Deploying or upgrading a probe

One command from the repository root. It builds, uploads, installs the systemd units
and enables the timers. Idempotent — the same command upgrades an existing probe.

```bash
RNFO_DEPLOY_PASSWORD='...' python tools/deploy.py root@<host> <probe-id> <hosting|eyeball|mobile>
```

For an ARM device such as the Android probe:

```bash
python tools/deploy.py root@<host> ru-mobile mobile --arch arm64
```

Add `--run` to trigger a control run immediately after installing.

Every probe receives the identical binary and identical units. The only file that
differs is `/etc/rnfo/probe.env`, holding the probe id and network type. If you find
yourself editing anything else on one probe only, stop: two probes that differ are two
instruments, and their data cannot be compared.

The installer keeps a timestamped backup of the previous `probe.env`, so an identity
correction is reversible.

### After deploying

1. Add the probe to `probes.yaml` with `status: active` and its observed ASN.
2. Add `RNFO_SSH_<probe_id_with_underscores>=root@<host>` to `.env`.
3. Confirm the first run: `journalctl -u 'rnfo-probe@*' -n 30 --no-pager`.

---

## Schedule

| Timer | When (UTC) | Targets |
|---|---|---|
| `rnfo-probe-full.timer` | 00:00, 06:00, 12:00, 18:00 | 2 824 URLs |
| `rnfo-probe-controls.timer` | every 15 minutes | 10 controls + 4 own (responder ports) |

Both are `Persistent=true`, so a run missed while the machine was off fires on boot
and the gap appears in the data as a late run rather than as silence.

Both use `AccuracySec=1s` with no randomised delay. This is deliberate and must not be
"tidied up": systemd's default one-minute accuracy window would scatter probes across
it, and simultaneity between a Russian probe and its foreign control is the entire
point of having a control.

`OnCalendar` carries an explicit `UTC`, so probes fire at the same instant regardless
of host timezone. `systemctl list-timers` renders that instant in local time, so the
Dutch host prints `22:30 CEST` for the same slot the Moscow host prints as `20:30 UTC`.
Same moment, different rendering; do not "fix" it.

A workstation clock is not evidence. When checking whether a slot has fired, compare
against `date -u` **on the probe**: the probes are NTP-disciplined and a desktop
frequently is not. This one was 83 seconds fast on 2026-09-04, which is enough to make
a slot look missed when it simply had not arrived.

Run one by hand:

```bash
systemctl start rnfo-probe@controls.service     # ~5 seconds
systemctl start --no-block rnfo-probe@full.service   # tens of minutes
```

---

## Collecting

Pull-based. Probes hold no credentials and cannot reach the archive, so a probe that
is compromised or seized cannot rewrite history.

```bash
go build -o bin/rnfo-collect ./collector
./bin/rnfo-collect pull                # all active probes in probes.yaml
./bin/rnfo-collect pull -probe ru-msk-vps
./bin/rnfo-collect pull -live          # also today's unsealed files, as live-*.jsonl (provisional)
./bin/rnfo-collect validate            # schema + registry check on data/
./bin/rnfo-collect stats               # rows, verdict distribution, day coverage
./bin/rnfo-collect compare -slot 2026-09-05T00:00Z/full -subject ru-msk-vps -control nl-lim-panel
```

`compare` is the analysis the design exists for: one slot, joined target by target.
"Failed from subject only" is the only bucket attributable to the subject's network.
"Failed from control only" is a warning about the control's own address, and if that
bucket is large the control is not clean for those targets — see the de-fra-vps entry
in `probes.yaml` for a live example.

`pull` fetches only sealed days — a day file with a `.sha256` sidecar, meaning the
probe has finished writing it — verifies the checksum, and **rejects** any file that
does not match rather than admitting unverifiable rows.

`validate` exits non-zero if anything is wrong, so it belongs in CI before any
publication.

**Automated on the workstation.** Windows Task Scheduler runs `tools/pull.cmd` daily
at 03:30 local as task `RNFO daily pull`: pull, then validate, appending to
`data/logs/pull.log`. Check it with `schtasks /Query /TN "RNFO daily pull"`; remove it
with `schtasks /Delete /TN "RNFO daily pull" /F`. Data also accumulates on each probe
for 90 days, so a workstation that is off for a week loses nothing. A collector on a
machine that is always up is still the right long-term home; this is the bridge.

---

## Adding a target

Do not, without deciding it deliberately. Targets are the Citizen Lab lists plus our
own endpoints, and that boundary is what makes the ethics statement true.

**Refreshing the pinned lists** (an event, not a routine):

```bash
bash tools/fetch_test_lists.sh          # re-pins and rewrites probe/lists/MANIFEST.json
go build ./...                          # lists are compiled into the binary
# redeploy every probe, then record the change in docs/CHANGELOG-lists.md
```

Refresh all probes together. Probes measuring different target sets produce data that
looks like a change in the network and is not.

**Our own endpoints** go in a CSV given to the agent with `-own`, so a responder can be
added without a rebuild.

---

## Troubleshooting

**`controls_intl_*` is null in a run record.** The run was written by agent 0.1.0,
before the international-only health rule. Its `healthy` was judged on all ten
controls. Not an error; the `agent` field is there so this is unambiguous.

**Unit failed with status 3.** Not a crash. Exit 3 means the connectivity controls
failed, so the probe was off the network. Check the run record for that slot.

**`no_address_family` everywhere.** The probe has no address of the family in
`RNFO_IP_FAMILY`. This is a configuration difference, not blocking, and the verdict is
separate precisely so the two are never confused.

**Everything is `tls_timeout`.** Check the controls first. Domestic controls up and
international controls down is an upstream problem, not a discovery.

**Clock.** `chronyc tracking` on each probe. Offset must stay in single-digit
milliseconds or the two-sided captures in Tier 2 cannot be correlated. Hosts running
`systemd-timesyncd` instead of `chrony` are adequate for Tier 1 and must be switched
before Tier 2.

---

## Responder

Deployed 2026-09-04 on `de-fra-vps`, the only dedicated foreign host. Certificate
fingerprint and ports are in `probes.yaml`; address and token are in `.env`.

```bash
python tools/deploy_responder.py root@<host>        # install or upgrade; token from .env
ssh root@<host> 'systemctl status rnfo-responder; ufw status'
```

Every probe dials the responder on all four TLS ports in every controls run (the
`own` list), so address-level reachability of our endpoint is measured continuously
and a block of the address shows up within fifteen minutes.

### The SNI experiment

Same address, ten server names, five interleaved rounds, roughly four minutes. Run
it on the Russian probe and on a foreign control **with the same `-run-id`** so the
rows join. The simplest way is a tiny wrapper on each host:

```bash
RID="$(date -u +%Y-%m-%dT%H):00Z/sni"
cat > /root/sni.sh <<EOF
#!/bin/sh
exec runuser -u rnfo -- env \$(grep -v '^#' /etc/rnfo/probe.env | xargs) \
  /opt/rnfo/bin/rnfo-probe -experiment sni -responder <responder-ip> -repeats 5 -run-id '$RID'
EOF
chmod +x /root/sni.sh && nohup /root/sni.sh > /root/sni.log 2>&1 &
```

It prints a per-name summary at the end and writes rows with `list: sni`. Names come
only from the pinned lists plus our own; no packet reaches the named hosts.

Endpoints:

| Path | Purpose |
|---|---|
| `/v1/health` | liveness |
| `/v1/echo` | reports what the server received — diff against what was sent to detect rewriting in the path |
| `/v1/bytes?n=&chunk=&delay=` | emits an exact volume at controlled pacing — this is how the reported ~16 KB cut-off gets measured instead of guessed at |

Connections without the token are counted per port and otherwise discarded. See
`ETHICS.md` §4.
