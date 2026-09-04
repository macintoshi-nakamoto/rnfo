# Russian Network Filtering Observatory

An open, longitudinal dataset of how network filtering behaves inside Russia,
measured from several vantage points, with a documented methodology and a published
ethics statement.

This is a **measurement** project. It observes and records how networks behave. It
does not build, ship or document tools for evading network controls, and none are
present in this repository. See [`docs/ETHICS.md`](docs/ETHICS.md).

**Status: collecting.** First production data 2026-09-04 from one Russian probe.
One foreign control is still missing, and until it exists nothing here is publishable
— see [Open defects](#open-defects).

---

## Why this exists

Several projects measure Russian network filtering, and each stops short of a
different thing:

- **OONI** collects global web-connectivity data, but does not characterise filtering
  behaviour at the protocol level.
- **GlobalCheck** runs a volunteer sensor network, but exposes a service — "is site X
  reachable now" — rather than a downloadable historical dataset.
- **blockcheck** classifies blocking type well, and is currently unmaintained.
- The tooling on ntc.party is excellent, and it is tooling: no control group, no
  confidence intervals, no ethics statement.

What is missing is a machine-readable longitudinal dataset with a research-grade
methodology, covering both eyeball and hosting networks, that records *how* a
connection died rather than only *that* it died.

The methodological template is Xue, Mixon-Baca, ValdikSS, Ablove, Kujath, Crandall and
Ensafi, *TSPU: Russia's Decentralized Censorship System*, IMC '22.

---

## What it measures, and why not `curl`

The obvious implementation is a shell script around `curl` and its exit code. This one
is not, and the reason is the point of the project.

`curl` exit code 56 means "connection reset by peer". It is returned whether the reset
arrived immediately after the TCP handshake, right after the TLS ClientHello — the
first packet carrying the server name in the clear — or halfway through the response
body. Those are three different filtering mechanisms and `curl` cannot tell them
apart.

The agent performs the connection in explicit stages and records where it died, what
the kernel said, and **how many bytes arrived first**:

```json
{"schema":"v1","run_id":"2026-09-04T18:00Z/full","probe":"ru-msk-vps","net":"hosting",
 "asn":"AS203273 NetCrafters OU","target":"www.bbc.com","list":"citizenlab-global",
 "stage":"tls","verdict":"tls_reset","errno":"ECONNRESET","curl_rc":35,
 "t_connect":0.040,"bytes_read":0,"ip_family":"v4"}
```

TCP completed in 40 ms; the connection died during the TLS handshake with a reset and
zero bytes of application data. That shape is consistent with name-based inspection,
and it is distinguishable from the 88 targets in the same run that were silently
dropped instead, and from the 13 that never completed a TCP handshake at all.

`bytes_read` counts wire bytes, which is what makes the reported "connections are cut
after roughly 14–25 KB" behaviour measurable rather than anecdotal.

`curl_rc` is kept in every row as a compatibility field, so this dataset can still be
compared against curl-based measurements published elsewhere.

---

## Layout

```
probe/          measurement agent; lists/ are pinned and compiled into the binary
responder/      controllable endpoint: any SNI, exact response sizes, chosen pacing
collector/      pull, verify checksums, validate against the schema and registry
internal/       schema, failure classification, targets, identity, registry
docs/           methodology, ethics, schema, operations
probes.yaml     the probe registry; a row with an unregistered probe id is invalid
```

---

## Quick start

```bash
go build ./...

# see what one probe would measure, without writing anything
go run ./probe -probe local-test -net eyeball -profile controls -dry-run

# deploy to a probe (idempotent; also upgrades)
python tools/deploy.py root@<host> <probe-id> <hosting|eyeball|mobile>

# collect and check
./bin/rnfo-collect pull
./bin/rnfo-collect validate
./bin/rnfo-collect stats
```

Full detail in [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

---

## Design rules worth knowing before reading the data

- **Every attempted target produces a row, including failures.** A missing row means
  the probe was down, and the run records prove it.
- **Run ids are the scheduled slot, not the start time**, and every probe fires on the
  same UTC schedule with a one-second accuracy window. A Russian measurement and its
  foreign control therefore share a run id and join on `(run_id, url)`.
- **`healthy: false` runs must be discarded.** Ten connectivity controls, half of them
  domestic, run at the start of every measurement. If they fail, the probe was off the
  network and the rest of that run says nothing about filtering.
- **Hosting is not eyeball.** Russian filtering equipment sits mainly at operators
  serving subscribers. A datacentre probe and a household probe measure different
  networks, and pooling them into one "Russia" number is a mistake the `net` field
  exists to prevent.
- **IPv4 only, by decision.** Probes differ in whether they have working IPv6, and
  letting the resolver choose would silently make each probe a different experiment.
  `no_address_family` is a distinct verdict, never confused with a block.

---

## Open defects

Listed here rather than in an appendix, because they bound what the data can support.

1. **No foreign control is running.** Every Russian measurement currently lacks its
   simultaneous outside measurement. Nothing may be published until this is fixed.
2. **The foreign machines are one network, not four.** All are in AS200823 and three
   share a /24, so "does filtering follow the prefix or the AS" has a sample size of
   one on the axis that matters. Fixing it requires one machine at a different
   provider, not more machines at this one.
3. **No dedicated foreign host, so Tier 2 and Tier 3 have not started.** A responder
   on an address that already carries production traffic would measure that traffic,
   not the experiment.
4. **One Russian vantage point, on the less interesting network type.**

Full discussion in [`docs/METHODOLOGY.md`](docs/METHODOLOGY.md) §7 and §10.

---

## Ethics in one paragraph

Targets are the public Citizen Lab `global` and `ru` test lists, pinned by commit hash,
plus endpoints this project owns. Nothing else is ever contacted: no scanning, no
enumeration. One request per target per probe per six hours. No response bodies are
stored, only a hash and the page title. No personal data is collected from anyone. The
probes' own addresses are never written to the dataset — the validator rejects rows
that contain one. Probes on machines the project does not own require recorded consent,
and the registry loader refuses to mark such a probe active without it. Full statement:
[`docs/ETHICS.md`](docs/ETHICS.md).

To have a host excluded from measurement, or to ask anything about the data, use the
contact address below. Exclusion requests are honoured without argument and recorded in
the repository, so the resulting gap in the data is visible and explained.

---

## Licence and contact

Code and dataset: to be released under an open licence before first publication.

Contact: *(add a project address here before the repository is made public — the ethics
statement promises one)*
