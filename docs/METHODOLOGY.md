# Methodology

Version 0.1, 2026-09-04. This document describes what the instrument measures, what
it deliberately does not measure, and where the current design is weak. It is written
to be read by someone deciding whether to trust the dataset.

---

## 1. Question

How does network filtering inside Russia behave, at the protocol level, over time,
and how does that behaviour differ between network types and between destinations?

Three sub-questions, in the order the project can answer them:

1. **What fails, and how?** Not "is this site blocked" but "at which stage of the
   connection does it die, and what does the network do to kill it".
2. **Does the mechanism depend on the network the user is on?** A datacentre uplink
   and a residential line are not the same network and are not filtered by the same
   equipment.
3. **Does the mechanism depend on the destination?** On the name, on the address, on
   the prefix, on the autonomous system, on the port, or on the volume of data.

---

## 2. Unit of measurement

One **measurement** is one URL, from one probe, at one moment, carried to completion
or to failure. It always produces exactly one row, including when it fails. This is
the load-bearing rule of the dataset: absence of a row never means "nothing happened",
it means the probe was not running, and the run records prove which.

Measurements are grouped into **runs**. A run is one execution of the agent over one
target profile. Every run writes a run record with start time, finish time, target
count, verdict histogram, and control health.

Runs are identified by their **slot**, not by their start time: the run id is the
scheduled time floored to the profile period (six hours for `full`, fifteen minutes
for `controls`). Every probe uses the same UTC schedule with `AccuracySec=1s`, so a
Russian measurement and its foreign control land in the same slot and share a run id.
Pairing them is then a join on `(run_id, url)` rather than a guess about timing.

---

## 3. Staged measurement, and why not curl

The obvious implementation is a shell script around `curl` and its exit code. The
brief specifies exactly that, and the exit codes remain in the dataset as `curl_rc`
so this data can be compared with curl-based measurements published elsewhere. But
`curl` is not the instrument, because it answers the wrong question.

`curl` exit code 56 means "connection reset by peer". It is returned whether the
reset arrived:

- immediately after the TCP handshake, before anything identifying was sent;
- right after the TLS ClientHello, the first packet carrying the server name in
  the clear;
- in the middle of the response body, after tens of kilobytes.

Those are three different filtering mechanisms - address-based, name-based, and
volume- or content-based - and they are indistinguishable in curl's output. The agent
therefore performs the connection in explicit stages and records:

| Field | Meaning |
|---|---|
| `stage` | how far the connection got: `dns`, `tcp`, `tls`, `request`, `response`, `ok` |
| `verdict` | closed vocabulary combining stage and mechanism, e.g. `tls_reset` |
| `errno` | the kernel-level error: `ECONNRESET`, `ETIMEDOUT`, `EHOSTUNREACH`, … |
| `bytes_read` | wire bytes that arrived before the connection died |
| `t_connect`, `t_tls` | handshake durations as separate deltas, not cumulative |

`bytes_read` deserves its own note. The reported behaviour where connections are cut
after roughly 14–25 KB cannot be studied with a tool that reports only "the transfer
failed". Counting wire bytes at the socket makes the cut-off point a number in every
row, which turns an anecdote into a distribution.

The first production run from Moscow, 2026-09-04, already separates mechanisms that
curl would have merged: 88 targets timed out *during the TLS handshake* while 13
timed out *at TCP connect*, and 5 were *reset* during the handshake after a 40 ms TCP
handshake had succeeded. Same curl exit codes; three different network behaviours.

### What the staged design does not prove

A reset observed at the TLS stage is *consistent with* name-based blocking. It is not
proof. The server itself may have reset the connection, and a shared address may be
blocked for reasons unrelated to the name requested. Distinguishing these requires
either the TTL analysis or the responder experiments, both of which are Tier 2. Until
then, Tier 1 verdicts describe **behaviour**, not **intent**, and the analysis must
use that language.

---

## 4. Controls

### 4.1 The foreign control

Every measurement from inside Russia is meaningless without a simultaneous
measurement from outside it. A site can be unreachable because it is filtered, or
because it is down, or because it blocks datacentre addresses, or because its
certificate expired. Only the foreign control separates those.

Since 2026-09-04 two foreign controls run on the same UTC schedule as the Russian
probe, with the same binary, the same target list and the same one-second accuracy
window, so all three land in the same slot and share a run id.

A control is only as good as its own reachability, and that is measured rather than
assumed: in every paired comparison the bucket "failed from the control only" is
reported, and it is the control's cleanliness score. On the first paired slot:

| Control | AS | Dedicated | Failed from control only |
|---|---|---|---|
| `nl-lim-panel` | AS200823 | no | 7 of 2 824 (0.2 %) |
| `de-fra-vps` | AS210644 | yes | 262 of 2 824 (9.3 %) - almost all Russian-hosted targets |

So the two controls have different jobs. `nl-lim-panel` is the primary control for
every target. `de-fra-vps` is a valid control for the international list only, and for
Russian-hosted targets it is not a control at all but a *subject*: its failures toward
Russia are a finding (section 8), not a baseline. Both roles are written on the
registry entries.

`nl-lim-panel` is not a dedicated machine; it runs a production web service. That is a
documented deviation from the dedicated-probe rule, taken because the agent is outbound
HTTP only and opens no listening port. It does not extend to the responder, which lives
only on `de-fra-vps`.

### 4.2 The connectivity control set

Ten targets, five domestic and five international, are measured at the start of every
run and again every fifteen minutes. They exist to answer a different question from
the study: *was the probe on the network at all?*

A run where most controls fail says nothing about filtering, and the run record
carries `healthy: false` so that analysis discards it rather than reading a local
outage as a wave of blocking. The threshold is half the control set; the raw counts
are recorded so a different threshold can be applied later without re-measuring.

Domestic and international controls are separated on purpose. Domestic controls up
and international controls down is a different failure from everything down, and the
distinction is visible without any extra instrumentation.

### 4.3 The confirmation retry

A target that fails is measured once more, three seconds later, recorded as
`attempt: 2`. A failure that reproduces immediately is much stronger evidence than a
single observation, and the two rows let the analysis quantify how much of the
observed failure rate is transient.

The retry is skipped when the run is not `healthy`, that is, when the international
connectivity controls (section 4.2) failed in the first pass. A probe without a network
would otherwise retry every target, prove nothing, and double the load placed on the
test-list sites for no information.

Until agent 0.4.2 the guard was different: skip the retry when more than 40 % of the
first pass failed. That threshold was chosen with the hosting probe in mind, where a
quarter of the list fails. On the residential line the ordinary failure rate is
40-41 %, so the guard fired on every full run and none of that probe's failures were
confirmed between 2026-09-05 and 2026-09-08 (limitation 10). The lesson is recorded
here because it is a methodological one: a fixed share of failures cannot distinguish
"the network is down" from "the network filters a lot", and the controls exist
precisely to make that distinction. From 0.4.3 they gate the retry. The change does
not alter any field's meaning; `attempt: 2` rows simply exist for the residential probe
from that version on, and their absence before it must be read as "not retried", not
as "did not reproduce".

---

## 5. Deliberate choices that constrain interpretation

Each of these is a decision that makes the dataset internally consistent at the cost
of generality. They are listed so that a reader knows what the numbers do and do not
cover.

**Address family is fixed to IPv4.** The Moscow VPS has working IPv6 and its
resolver returns AAAA records first; a home broadband line or an Android handset
frequently has no IPv6 at all. Left to the resolver, one probe would measure IPv6 and
another IPv4, and the comparison between them would be meaningless. Every row records
`ip_family`, and a v4-versus-v6 comparison is a deliberate separate run, never a
silent difference between machines. A host with no address of the requested family
produces the verdict `no_address_family`, which is explicitly *not* a blocking
verdict - mistaking one for the other would manufacture censorship out of a
configuration difference.

**ALPN offers `http/1.1` only.** HTTP/2 would introduce a second connection-handling
code path that swallows socket-level errors, which is precisely what we are trying to
observe. The cost is that HTTP/2-only behaviour is invisible to this dataset.

**The User-Agent is an ordinary browser string.** A distinctive research User-Agent
would be more polite, and it is what a courteous scanner does. It is not what this
instrument can afford: a middlebox that treats an unknown User-Agent differently
would bias the measurement away from what a real user experiences. The project
identifies itself in a different way - through this repository, a published contact
address, and reverse DNS on the probes - rather than by biasing its own measurement.
See `ETHICS.md`.

**Certificates are fingerprinted, not enforced.** The agent completes the TLS
handshake with verification disabled and records the leaf certificate's hash, subject,
issuer and whether it matches the requested name. Aborting on an invalid certificate
would hide the interception it would be evidence of. Note the converse trap:
`cert_name_ok: false` on a row whose verdict is `ok` usually means the site is simply
misconfigured, not that anything intercepted it. Interception evidence requires an
unexpected issuer, a fingerprint that differs from the foreign control's for the same
host in the same slot, or both.

**Bodies are not stored.** Only length, the SHA-256 of the first 64 KiB, and the
`<title>`. That is enough to recognise a block page and to detect that two probes
received different content for the same URL, and it means the dataset carries no
third-party content.

**Target order is shuffled per slot with a seed derived from the run id.** The order
is therefore identical across probes in the same slot - so pairing stays tight - and
different between slots, so no site is permanently measured first. Without this, every
result for the first site in the list would be correlated with whatever happens at the
start of a run.

---

## 6. Vantage points

Probes are registered in `probes.yaml`, which is the authority on what a probe id
means. The validator rejects any row whose probe id is not registered, so a
measurement can never enter the dataset without a documented vantage point.

**Hosting is not eyeball.** Russian filtering equipment is deployed principally at
operators serving subscribers. A datacentre uplink is expected to see substantially
less filtering than a residential line. That difference is a *result* of this study,
not an inconvenience, and the two must never be pooled into a single "Russia" figure.
The `net` field exists to make pooling them a deliberate act.

---

## 7. Vantage point independence - the open defect

This section exists because the inventory in the brief and the inventory that exists
are not the same thing, and the difference determines which questions can be answered.

Observed on 2026-09-04:

| Host | Country reported | Autonomous system | Dedicated to this project |
|---|---|---|---|
| Moscow VPS | RU, Moscow | AS203273 NetCrafters OU | yes |
| "Netherlands" panel | NL, Limburg | **AS200823 MHost LLC** | no - production service |
| "Netherlands" node | NL, Limburg | **AS200823 MHost LLC** | no - production service |
| "Germany" node | DE, Frankfurt | **AS200823 MHost LLC** | no - production service |
| "Poland" node | seller says PL, geolocation says LT | **AS200823 MHost LLC** | no - production service |

Three consequences.

**The foreign side is one network, not four.** Every foreign machine is in AS200823,
and three of them share the prefix 103.114.43.0/24. The question the brief calls out
as important - *does filtering depend on the destination prefix or the destination
AS?* - has a sample size of one on the AS axis. Three prefixes give a weak test of
the prefix axis and no test at all of the AS axis. No number of additional machines
at this provider will fix this; it requires a machine at a different provider.

**None of the foreign machines is clean.** All four run a production VPN service with
live users. This breaks the "probes are dedicated machines" constraint, and it does
something worse to the science: a Tier 3 experiment that measures how long a transport
survives against one of these addresses is not measuring the transport. It is
measuring an address that already carries that exact transport for real users. The
result would be uninterpretable, and a positive result - successfully provoking a
block - would take a production service down.

**The "Poland" machine is not in Poland.** The seller advertises Warsaw. Cloudflare,
ipinfo and ipwho.is all place it in Lithuania; the whois record says PL and the
organisation is registered in Georgia. Whatever it is, its geolocation is contested,
and a dataset field that says "PL" because an invoice said so is a fabricated
measurement. Country is recorded as observed, with the disagreement documented, or
the machine is not used for any claim that depends on location.

### What was done about it (2026-09-04)

A dedicated VPS was bought in **AS210644**, in Frankfurt - the same city as the
project's AS200823 node, so that a comparison between the two destinations holds
geography roughly constant and varies the network. It carries nothing but the agent and
the responder. This closes the "no clean host" problem and gives the AS axis a sample
size of two, which is the minimum at which the question exists.

Three things about it are recorded so nobody over-reads the comparison:

- **The two ends are less independent than their ASNs suggest.** The Moscow probe's
  AS203273 and the Frankfurt host's AS210644 share an upstream, AS216246, and both
  carry hostnames under the same `ptr.network` domain. The RIPE holders differ; the
  operating provider behind them may not. This does not affect the destination-AS
  comparison (AS210644 versus AS200823, both in Frankfurt, are genuinely different
  operators), but it does mean Moscow-to-Frankfurt experiments run between two hosts
  that may share a provider.
- **The paths differ in length.** From Moscow, 11 hops to the AS210644 host against 7
  to the AS200823 host, diverging at the fourth hop, both through public transit. A
  difference in filtering between the two destinations may therefore be a path effect
  rather than an AS effect. The TTL analysis in Tier 2 is what separates those.
- **The new host is not a clean control for Russian destinations**, and the reason is
  itself the first result of the AS comparison: see section 8.

---

## 8. First controlled results - slot 2026-09-04T18:00Z

Provisional: from `live-` files, one slot, one Russian vantage point on a hosting
network. Reported here because the methodology should be judged against what it
actually produces, not to make claims about Russia.

### 8.1 Russia versus the primary control

2 824 targets paired between `ru-msk-vps` (Moscow, AS203273) and `nl-lim-panel`
(Netherlands, AS200823), zero unpaired.

| Bucket | Targets | Share |
|---|---|---|
| ok from both | 1 950 | 69.1 % |
| **failed from Moscow only** | **712** | **25.2 %** |
| failed from both | 155 | 5.5 % |
| failed from the control only | 7 | 0.2 % |

The 712 Moscow-only failures are the only ones attributable to the Russian network.
Every one of them was retried three seconds later and failed again: **100 % reproduced**.
By mechanism: `tls_timeout` 599 (84 %), `connect_timeout` 43, `request_timeout` 30,
`tls_reset` 17. By list: 434 from the `ru` list (40 % of it), 278 from `global`. By
category: NEWS 318, ANON 63, HUMR 37, GRP 36, HOST 32, LGBT 32.

What this does and does not say. It says that from one Russian hosting network, a
quarter of the standard test list is unreachable in a way that reproduces immediately
and is overwhelmingly a silent drop during the TLS handshake rather than an injected
reset. It does not say this is representative of Russia - hosting is not eyeball - and
it does not say the drop is name-based rather than address-based; that is what the SNI
experiment below is for.

### 8.2 The second autonomous system, and an inbound finding

The same slot paired against `de-fra-vps` (Frankfurt, AS210644) gives 705 Moscow-only
failures - consistent with the 712 above - but **262 targets failed only from
Frankfurt**, against 7 for the Dutch control. Pairing the two foreign hosts directly:
266 targets fail from AS210644 and succeed from AS200823, 260 of them `connect_timeout`,
241 of them on the Russian list.

Direct TCP tests between hosts the project controls, all with the port open in the
destination's firewall (a correction is recorded below):

| From | To | TCP | ICMP |
|---|---|---|---|
| Frankfurt AS210644 | Moscow probe AS203273, port 22 | **dropped** | answers, 38 ms |
| Frankfurt AS210644 | three Russian sites from the list, port 443 | **dropped** | - |
| Frankfurt AS210644 | Dutch AS200823 host, port 22 | answers | - |
| Moscow probe AS203273 | Frankfurt AS210644, ports 443 and 22 | **dropped** | answers (11 hops) |
| Moscow probe AS203273 | AS200823 hosts in Frankfurt and the Netherlands, 443 | answers | - |
| Dutch AS200823 | Moscow probe, port 22; Frankfurt AS210644, 443 and 22 | answers | - |
| AS200823 node in Frankfurt | Moscow probe, port 22; Frankfurt AS210644, 443 | answers | - |

So TCP between the AS210644 Frankfurt prefix and the Russian networks tested is dead
**in both directions**, ICMP passes, and the same prefix exchanges TCP with AS200823
hosts in the same city without trouble. The failures toward Russia spread across 168
distinct /16 destination networks, with the ten largest holding only 26 % of them -
not the signature of individual sites blocklisting a provider, which would cluster,
but of a TCP-specific drop on the path between that prefix and Russia. The condition
predates any experiment of ours: the first full run from Frankfurt, started before the
responder existed, already showed it.

**Correction.** An earlier draft of this section stated that the reverse direction,
Moscow to Frankfurt, worked, on the grounds that the SNI experiment had started over
it. That was an inference from a process starting, not a measurement, and the
measurement contradicts it: every one of the experiment's fifty connection attempts
timed out at the TCP handshake. The claim is withdrawn here rather than silently
edited, because the dataset's value rests on that habit.

What it does not prove. Russian-side filtering of the prefix (the "whole hosting
prefixes blocked" behaviour the brief asked about) and a TCP-only egress policy at the
source provider's transit toward Russia would both look like this from where we stand.
The discriminator is a Russian network that lets the prefix's TCP through: per-operator
variation is a Russian-side signature, uniform failure is a source-side one. The
project has exactly one genuine Russian vantage point, so this cannot be settled yet.
A test from the owner's workstation looked as if it settled it - the Frankfurt host
answered - until the workstation's egress was checked: it leaves through the owner's
own tunnel and exits at an AS200823 node. That result was a measurement of AS200823,
not of Russia, and it is discarded. It is also the cleanest illustration available of
the vantage point rule (docs/METHODOLOGY.md §5).

Consequence for the design: `de-fra-vps` is a valid control for the international list
and a subject, not a control, for Russian-hosted targets. `nl-lim-panel` remains the
primary control.

### 8.3 Hosting versus eyeball: the first residential slot

Slot `2026-09-05T12:00Z/full`, the first full run from a residential line
(`ru-mow-home`, Vimpelcom broadband, AS8402), paired against the Dutch control and
against the Moscow hosting probe in the same slot.

| Comparison | Failed from subject only |
|---|---|
| Moscow hosting vs Dutch control | 715 (25.3 %) |
| **Home broadband vs Dutch control** | **1 005 (35.5 %)** |
| Home broadband vs Moscow hosting | 302 (10.7 %) |

So the residential line loses about ten points more of the list than the datacentre
uplink does, in the same six-hour window. The 302 targets that fail from home but not
from the Moscow VPS are the eyeball-only component, and its mechanism is not the one
that dominates from hosting: 217 of the 302 are `response_timeout`, a connection that
completed the handshake, sent its request, began receiving a response and then
stalled. From hosting, the dominant mechanism is `tls_timeout`, a drop before any
application data. 244 of the 302 are on the international list.

What this does and does not say. It is one slot from one household on one operator,
and a stall can also be congestion or a slow site; the control rules out the site being
down but not the site being slow for everyone at that hour. It is consistent with
traffic shaping applied at the subscriber edge rather than at the transit edge, which
is where the literature places that behaviour, and the stall point (`bytes_read`)
across many such connections is the measurement that would show it. That analysis is
next; the number above is reported so the reader can see what the design produces
before it is polished.

### 8.4 Same address, different names - inconclusive, and why

The responder accepts any server name and returns the same certificate. From the Dutch
control, all ten names × five rounds completed (50/50 `ok`): outside Russia the name
does not matter, which is the baseline the experiment needs.

From Moscow, run id `2026-09-04T21:00Z/sni`: **50 of 50 attempts timed out at the TCP
handshake**, for every name including the trial that sends no name at all, with zero
bytes exchanged. The `own` targets confirm it every fifteen minutes: all four responder
ports, `connect_timeout` from Moscow, since the first run that carried them.

The experiment therefore says nothing about names. It cannot: the address is
unreachable at a layer below the one where a name is sent. What it does say is that the
responder, as placed, is unusable from the project's Russian vantage point, and that
every Tier 2 and Tier 3 experiment that needs a Moscow-to-responder connection is
blocked until a responder exists on a prefix that Russian networks pass TCP to.

Two things are kept from this. The Frankfurt host stays where it is as a control for
the international list and as the *subject* of a continuous measurement: the `own`
rows from Moscow every fifteen minutes are a longitudinal record of whether the prefix
block persists, lifts, or changes ports - the kind of series that is only obtainable
by leaving an instrument in place. And the SNI experiment is ready to run the moment a
reachable responder exists; the code and the name rule do not change.

### 8.5 Three sealed days, 2026-09-05 to 2026-09-07

The first numbers from verified day files rather than `live-` copies. Twelve full slots
paired against the Dutch control, first attempts only, all three subjects in the same
slots with the same list.

| Subject vs `nl-lim-panel` | Paired rows | ok from both | failed from subject only | failed from both | control only |
|---|---|---|---|---|---|
| `ru-mow-home` (residential, AS8402) | 28 280 | 58.9 % | **35.2 %** | 5.5 % | 0.3 % |
| `ru-msk-vps` (hosting, AS203273) | 33 936 | 67.6 % | **26.3 %** | 5.6 % | 0.4 % |
| `de-fra-vps` (Frankfurt, AS210644) | 33 936 | 84.7 % | 9.3 % | 5.7 % | 0.3 % |

The single-slot figures of 8.1 and 8.3 hold across three days with no visible drift:
the residential line loses about nine points more of the list than the datacentre
uplink, in every slot. The Frankfurt row is the prefix block of 8.2 seen as a series:
97 % of its subject-only failures are `connect_timeout`, they are the Russian-hosted
targets, and the share has not moved.

By mechanism, subject-only failures:

| Mechanism | `ru-mow-home` | `ru-msk-vps` |
|---|---|---|
| `tls_timeout` | 6 562 (65.9 %) | 7 316 (81.8 %) |
| `response_timeout` | 2 195 (22.0 %) | 102 (1.1 %) |
| `request_timeout` | 562 (5.6 %) | 364 (4.1 %) |
| `connect_timeout` | 412 (4.1 %) | 737 (8.2 %) |
| `tls_reset` | 18 | 186 (2.1 %) |
| `dns_fail` + `dns_timeout` | 190 (1.9 %) | 24 |

The hosting network is almost a single mechanism: a silent drop after the ClientHello.
The residential line adds a second one that the hosting network practically lacks, a
connection that completes the handshake, sends its request, starts receiving and then
stalls, in one of every five subject-only failures. That is the behaviour the
literature attributes to shaping at the subscriber edge, and the residential probe is
the only instrument in this project that can see it. Its `bytes_read` distribution is
the next analysis.

By Citizen Lab category, share of first attempts that failed, all lists, three days:

| Category | `ru-mow-home` | `ru-msk-vps` | `nl-lim-panel` |
|---|---|---|---|
| NEWS | 58 % | 52 % | 7 % |
| POLR | 54 % | 45 % | 7 % |
| LGBT | 54 % | 42 % | 10 % |
| FILE | 49 % | 42 % | 11 % |
| GRP | 46 % | 41 % | 4 % |
| ANON | 39 % | 36 % | 4 % |
| HUMR | 38 % | 23 % | 6 % |
| REL | 24 % | 10 % | 4 % |
| PUBH | 21 % | 9 % | 8 % |

The Dutch column is the noise floor: list rot and sites that dislike datacentre
addresses. The gap between the two Russian columns is largest in the categories that
are least "political" (religion, public health, human rights), which is consistent
with the residential mechanism being coarser than the hosting one, but three days from
one household do not establish that.

Where the residential stalls stop. For the 3 044 `response_timeout` rows that failed
only from the residential probe over four days (2026-09-05 to 09-08), `bytes_read` is
the number of wire bytes that arrived before the connection went silent:

| bytes read before the stall | rows | share |
|---|---|---|
| under 8 KiB | 217 | 7.1 % |
| 8 to 20 KiB | 50 | 1.6 % |
| **20 to 32 KiB** | **2 217** | **72.8 %** |
| 32 to 64 KiB | 556 | 18.3 % |
| over 64 KiB | 4 | 0.1 % |

Median 29.7 KiB. The Dutch control read the same rows to a median of 56 KiB, so the
responses were longer than that; 2 686 of the stalls had already received a 200 status
line. 304 distinct targets, 89 % of them on the global list. The Moscow hosting probe
has 144 such rows in the same window and 97 % of them stop under 8 KiB, which is a
different shape entirely. So the residential line does not lose these connections at
random: they are cut inside a narrow band once roughly 20 to 32 KiB have been received,
which is the behaviour reported on ntc.party as "the 16 KB cut", here measured with a
control and a number. What decides which sites get it is the next question; the
target set is in the data.

One more series from the same days, because 8.4 stated it as absolute: the `own` rows
from the Moscow probe to the responder, every fifteen minutes. Successful dials per
day: 0 of 88 (09-04), **46 of 740 (09-05)**, 0 of 800, 0 of 800, 0 of 796, 1 of 392
(09-09, up to noon). The prefix block toward AS210644 is not absolute; it has opened
for a few hours once and for a single dial once. Whether that is a route change, a
device restart or something periodic is exactly the question a fifteen-minute series
can answer later and a one-off test never could.

Caveat that applies to every residential number above: none of them is confirmed by
a second attempt (limitation 10). The hosting numbers are: all 8 940 hosting failures
were retried three seconds later and 8 539 (95.5 %) failed again. The 4.5 % that
succeeded on retry are the transient share, and they are the reason the retry exists.

---

## 9. Load and politeness

The `full` profile covers 2 824 unique URLs: the pinned Citizen Lab `global` and `ru`
lists, ten connectivity controls, and our own endpoints. It runs four times a day per
probe at fixed UTC slots, with at most twelve concurrent requests and a random delay
of up to 250 ms before each.

Per target that is one request every six hours from each probe - less traffic than a
single person opening the page once. The lists are the standard research lists,
downloaded once and pinned by commit hash and SHA-256 so that any row can be traced to
the exact target set that produced it. No host outside the pinned lists and our own
endpoints is ever contacted. No enumeration, no range scanning, no port sweeps.

---

## 10. Reproducibility

- The target lists are compiled into the agent binary. The binary that produced a row
  fully determines which targets were measured, and a list refresh is a rebuild and a
  recorded event rather than a silent change under a running probe.
- Every run record carries `list_manifest`, mapping each list to the SHA-256 of the
  file the run used.
- Binaries are built with `-trimpath` so they are rebuildable from the tagged commit.
- Every probe runs the identical binary and identical systemd units. The only file
  that differs between deployments is `/etc/rnfo/probe.env`, which carries the probe
  id and network type. Two deployments differing in anything else are two different
  instruments and their data cannot be compared.

---

## 11. Known limitations

1. The foreign side is two autonomous systems, and one of them is not a clean control
   for Russian destinations (sections 7 and 8.2). The AS comparison has a sample size
   of two and shares an upstream between the Russian probe and one foreign host.
2. Two Russian vantage points, one hosting and one residential, on one operator each.
   Neither is representative of Russia; together they show that the network type
   matters, which is the point.
2a. Runs are paired by slot, not synchronised. On 2026-09-04 a Russian full run took
   roughly forty minutes while the control took two, because only the Russian side
   waits out timeouts. Rows join on `(run_id, url)` and the per-row timestamps give the
   skew. Adequate for reachability, inadequate for anything at packet granularity.
3. IPv4 only.
4. HTTP/1.1 only.
5. Tier 1 observes behaviour, not intent. Attribution of a reset to in-path injection
   requires the TTL analysis, which is not implemented.
6. The identity lookup depends on two third-party geolocation providers. When both are
   unreachable the run continues on a cached identity and records an
   `identity_unknown` event; the ASN in those rows is stale by up to three hours.
7. The responder is on a prefix the Russian vantage point cannot reach by TCP
   (section 8.3). Until a responder exists on a reachable prefix, the SNI experiment,
   the volume-trigger experiment and all of Tier 3 are not runnable from Russia.
8. The owner's workstation is inside the owner's own tunnel and is not a Russian
   vantage point. The residential probe is a separate handset, verified to be outside
   the tunnel from the Moscow probe's connection log.
9. Clock discipline is verified but not yet monitored. `chrony` on the Moscow probe
   reported an offset of −1.2 ms on 2026-09-04, which is fine for Tier 1 and adequate
   for Tier 2, but there is no alert if it drifts.
10. The residential probe's rows from 2026-09-05 to the full slot of 2026-09-08T12:00Z
   carry no `attempt: 2` rows, because the retry guard of agent 0.4.2 and earlier
   misread its ordinary failure rate as a lost uplink (section 4.3). Failure rates
   from that probe in that window are single observations. From 0.4.3 the retry is
   gated on the controls and runs there like everywhere else.
