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
and enables the timers. Idempotent - the same command upgrades an existing probe.

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

## Handsets (Termux): the eyeball and mobile probes

Android has no systemd, so on a handset the same binary runs as `rnfo-probe -daemon`
and keeps its own slot-aligned schedule (00/06/12/18 UTC for `full`, every quarter
hour for `controls`, catch-up on start like `Persistent=true`, no self-overlap). The
rows are indistinguishable from timer-driven ones; the run record's `agent` and an
`agent_started` event mark the daemon.

**What runs where.** The owner kept the realme Note 60 as a personal phone; the
POCO C51 is the research handset.

| Handset | Probe | Network | Notes |
|---|---|---|---|
| POCO C51 (Android 13 Go, **32-bit userspace**) | `ru-mow-home` | home Wi-Fi, **eyeball**, AS8402 Vimpelcom | live since 2026-09-05; binary is `linux/arm`, not arm64 |
| - | `ru-mobile` | SIM, Wi-Fi **off** | no handset assigned yet |

**Setup through USB instead of Wi-Fi.** The phone need not be reachable over the LAN
at all. With USB debugging on, `adb forward tcp:8022 tcp:8022` makes Termux's sshd
appear on the workstation as `127.0.0.1:8022`, and the deploy tool is pointed there
with `--lan-ip <phone's Wi-Fi address>` so it can still guess the router. ADB also
does the battery work the UI would otherwise need by hand:

```powershell
$adb = "$env:LOCALAPPDATA\Android\Sdk\platform-tools\adb.exe"; $s = "<serial from adb devices>"
& $adb forward tcp:8022 tcp:8022
foreach ($p in "com.termux","com.termux.boot","com.termux.api") {
  & $adb -s $s shell "dumpsys deviceidle whitelist +$p; cmd appops set $p RUN_ANY_IN_BACKGROUND allow"
}
& $adb -s $s shell "monkey -p com.termux.boot -c android.intent.category.LAUNCHER 1"   # Termux:Boot must be opened once
```

Run ADB from PowerShell, not Git Bash: Git Bash rewrites `/data/local/tmp` into a
Windows path and every adb argument breaks. If `adb` says "more than one device", an
emulator is running; pass `-s <serial>`.

**Architecture.** Ask Termux, not the CPU: `dpkg --print-architecture`. Budget phones
ship a 32-bit system on a 64-bit chip; the POCO C51 reports `armeabi-v7a`, `uname`
says `armv8l`, and an arm64 binary does not execute there. The deploy tool does this.

**Certificates.** Go verifies TLS against the system CA store and Termux has no
`/etc/ssl`, so the identity lookup (the thing that writes `asn` into every row) fails
silently with `identity_unknown` events. `probe.env` sets
`SSL_CERT_FILE=$PREFIX/etc/tls/cert.pem`. Measurements are unaffected either way,
because they fingerprint certificates rather than trust them - which is exactly why
this took a while to notice.

**Proving the phone is not in the owner's tunnel.** `rnfo-probe -whoami` prints the
ASN. If that is unavailable, make the phone connect to the Moscow probe and read its
sshd log: a TLS ClientHello against port 22 is logged as `banner exchange: Connection
from <phone's public address>`. The owner's own workstation shows up in the same log
from the AS200823 node, which is the contrast that settles it.

Both: plugged in permanently, nothing installed but Termux, Termux:Boot and Termux:API
from **F-Droid** (the Play Store builds are dead). **No VPN app on either phone**,
ever - the vantage point rule (docs/METHODOLOGY.md §5).

**Bootstrap, once, by hand in Termux on the phone:**

```bash
pkg update && pkg install -y openssh && passwd && sshd && ip addr | grep 'inet 192'
```

Then from the workstation on the same Wi-Fi (the tool refuses to proceed if the
phone's traffic leaves through AS200823, i.e. through the owner's tunnel):

```bash
RNFO_DEPLOY_PASSWORD='<passwd you set>' python tools/deploy_termux.py <phone-ip> ru-mow-home eyeball
RNFO_DEPLOY_PASSWORD='<passwd you set>' python tools/deploy_termux.py <phone-ip> ru-mobile  mobile --max-body 262144
```

The tool detects the CPU (arm64 expected), builds, uploads, writes `~/rnfo/probe.env`
and `~/rnfo/own.csv`, installs `~/.termux/boot/rnfo.sh`, puts the collector's key in
`authorized_keys`, and starts the daemon. After it, **by hand on the phone**, or Android
will kill everything within hours: Settings → Apps → Termux, Termux:Boot, Termux:API →
Battery → Unrestricted; open Termux:Boot once; Wi-Fi "keep on during sleep".

**DNS.** A static binary reads `/etc/resolv.conf`; Termux has none. The agent is
started with `RNFO_DNS` = the home router (detected as the default gateway), which
keeps the ISP's resolver in the path. On the SIM phone the carrier resolver is taken
from `getprop net.dns1` if present; otherwise pass `--dns`. Whatever was used is in
every run record as `resolver`.

**Metered SIM.** `--max-body 262144` caps the body read at 256 KiB per target (the
hash window is 64 KiB, so hashes stay comparable). At four full runs a day this is on
the order of a few hundred MB/day worst case; check the carrier plan. The cap is in the
run record as `max_body`.

**Checking a phone:**

```bash
ssh -p 8022 <phone-ip> 'tail -3 ~/rnfo/daemon.log; cat ~/rnfo/state/daemon.json; tail -1 ~/rnfo/data/runs/$(date -u +%F).jsonl'
```

**Collecting from a phone: the reverse tunnel.** A phone is behind NAT, so it cannot be
pulled from directly. Instead it keeps a reverse SSH tunnel open to the Moscow VPS,
and the collector pulls through that:

- On the VPS: account `rnfo-tunnel` (nologin, locked password), `authorized_keys`
  entry `restrict,port-forwarding,permitlisten="127.0.0.1:2201" <phone key>`, and
  `/etc/ssh/sshd_config.d/60-rnfo-tunnel.conf` with a `Match User rnfo-tunnel` block
  (`AllowTcpForwarding remote`, `PermitTTY no`, `ForceCommand /usr/sbin/nologin`,
  `ClientAliveInterval 30`). The phone can bind one loopback port there and do nothing
  else. One port per phone: 2201 is `ru-mow-home`; the next phone gets 2202 and its
  own key line.
- On the phone: `install.sh` generates `~/.ssh/rnfo_tunnel` and prints the public key
  to authorize; `rnfo-boot.sh` keeps `ssh -N -R 127.0.0.1:<port>:127.0.0.1:8022`
  running in a retry loop (`ExitOnForwardFailure=yes`, so a stale far-side listener
  becomes a retry, not a half-open tunnel). Log: `~/rnfo/tunnel.log`.
- On the workstation: `~/.ssh/config` has `Host rnfo-home` (HostName 127.0.0.1, Port
  2201, ProxyJump root@<moscow-vps>), and `.env` has `RNFO_SSH_ru_mow_home=rnfo-home`.
  `rnfo-collect pull` needs no change; `ssh rnfo-home` is the phone.

Pull semantics hold: the phone holds a key that can only open a forward to one
port on one host, and nothing that can read or write the archive.

Deploy with the tunnel in one go:

```bash
RNFO_DEPLOY_PASSWORD='...' python tools/deploy_termux.py 127.0.0.1 ru-mow-home eyeball \
    --lan-ip <phone-wifi-ip> --tunnel rnfo-tunnel@<moscow-vps> --tunnel-port 2201
```

**Clock.** Android network time is good to seconds, not milliseconds. Fine for Tier 1;
the handsets are not Tier 2 capture points.

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
bucket is large the control is not clean for those targets - see the de-fra-vps entry
in `probes.yaml` for a live example.

`pull` fetches only sealed days - a day file with a `.sha256` sidecar, meaning the
probe has finished writing it - verifies the checksum, and **rejects** any file that
does not match rather than admitting unverifiable rows.

`validate` exits non-zero if anything is wrong, so it belongs in CI before any
publication.

**Automated on the workstation.** Windows Task Scheduler runs `tools/pull.cmd` daily
at 03:30 local as task `RNFO daily pull`: pull, validate, `health -alert`, then mirror
the archive to the dedicated foreign host over the `rnfo-archive` ssh alias, appending
to `data/logs/pull.log`. The archive therefore exists in three places: on each probe
for 90 days, on the workstation, and on the mirror. It is not in git; publication is a
separate, versioned release under `data/LICENSE`.

**Workstation tasks and the logon type.** Both Task Scheduler jobs were created as
"run only when user is logged on" (interactive logon). That type fails with result
`-2147020576` whenever there is no interactive session, which is exactly when nobody is
watching. Switching them to a non-interactive logon (S4U, no stored password) needs an
elevated shell once. In a PowerShell started as Administrator:

```powershell
$p = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType S4U -RunLevel Limited
foreach ($t in "RNFO daily pull","RNFO health") { Set-ScheduledTask -TaskName $t -Principal $p }
```

Until that is done the workstation jobs run only while the user is logged on, and
`StartWhenAvailable` makes a missed run fire at the next opportunity. The watchers on
the servers do not depend on the workstation at all.

**Health and alerts.** `rnfo-collect health` runs one fixed status command on every
active probe and reports the newest controls run: stale after 45 minutes (three
slots), or unhealthy, or a clock more than five seconds off. `-responder` also fetches
`/v1/health` from the responder and compares the certificate with the fingerprint in
`probes.yaml`. Exit code 1 if anything is wrong.

With `-alert` and `RNFO_ALERT_URL` in `.env`, it messages on **changes**: a probe going
stale is reported once, again every six hours while it stays stale (`-repeat`), and
once more when it recovers. State lives in the `-state` file. A URL containing
`{text}` is fetched with GET after substitution, which is what a Telegram bot's
`sendMessage` wants; any other URL receives a JSON POST `{"text": ...}`. Telegram
detail that costs an hour if forgotten: a bot cannot message a person until that
person has opened the bot and pressed Start once.

Test the whole path by making everything look stale: `rnfo-collect health -alert
-stale 1s`, then run it again normally and expect the RECOVERED message.

**Watchers.** No single machine can see every probe, because the Frankfurt prefix does
not exchange TCP with Russia. So there are three:

| Where | Runs | Sees |
|---|---|---|
| Moscow probe, `rnfo-watch.timer`, hourly at :07 | `health -alert -probes ru-msk-vps,ru-mow-home,nl-lim-panel` | itself (`local`), the phone through its tunnel on `127.0.0.1:2201`, the panel host |
| Frankfurt host, same unit | `health -alert -probes de-fra-vps` | itself |
| workstation, Task Scheduler `RNFO health`, hourly | `health -alert -responder` | everything, plus the responder, while the workstation is on |

Each watcher holds its own key (`/root/.ssh/rnfo_watch`), authorised on the probes it
checks with `restrict,command="<status script>"`, so the key can run the status script
and nothing else. Install or update one with `tools/deploy_watch.py`; the probe side is
handled by the deploy tools when `RNFO_WATCH_PUBKEY` is in `.env`.

**Resolver fallback.** If the configured resolver (`RNFO_DNS`, the home router) stops
answering, the agent falls back to a public resolver at start, records
`resolver_fallback` as an event and the resolver actually used in every run record.
Measurements continue; DNS-related rows from those runs are measuring a different
resolver and the data says so. Check it with `schtasks /Query /TN "RNFO daily pull"`; remove it
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

**A phone stops reporting after a few hours.** Android killed Termux. Battery
optimisation was not disabled for Termux / Termux:Boot / Termux:API, or the wake lock
is missing (Termux:API not installed). The gap is in the data as missing slots; the
`agent_started` event marks the restart.

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

**SSH on the dedicated hosts** is key-only (`/etc/ssh/sshd_config.d/50-rnfo-hardening.conf`:
no passwords, root by key only, three tries). The panel host keeps its own policy.
The handset's sshd is reachable only through its reverse tunnel.

**Clock.** Every run record now carries `clock_offset_ms` from one SNTP exchange, so
drift is visible in the data. `chronyc tracking` on each server probe. Offset must stay in single-digit
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
| `/v1/echo` | reports what the server received - diff against what was sent to detect rewriting in the path |
| `/v1/bytes?n=&chunk=&delay=` | emits an exact volume at controlled pacing - this is how the reported ~16 KB cut-off gets measured instead of guessed at |

Connections without the token are counted per port and otherwise discarded. See
`ETHICS.md` §4.
