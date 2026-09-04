# Ethics statement

Version 0.1, 2026-09-04. Written before the first measurement was collected, not
after, and binding on the code: several of the rules below are enforced by the
validator and by the registry loader rather than left to discipline.

---

## 1. What this project is

A measurement study of network reachability. It observes and records how networks
behave. It does not develop, distribute, document or recommend tools for evading
network controls, and no such tool is present in this repository.

The distinction is not cosmetic. Measurement of network behaviour is ordinary
research, conducted openly by university groups worldwide. Promotion of circumvention
tools carries administrative liability in the Russian Federation. The project keeps
the boundary clean in code, in commit messages, in variable names and in
documentation, and reviewers are asked to enforce it.

Where the project measures the survivability of a transport protocol (Tier 3), it
measures a protocol's observable behaviour on the network. It does not publish
configurations, recommendations, or operational guidance for using those protocols.

---

## 2. Who and what can be measured

**Only two kinds of destination are ever contacted:**

1. Endpoints the project owns and operates.
2. The Citizen Lab test lists `global` and `ru`, pinned by upstream commit hash and
   SHA-256.

The lists are the standard instrument for this field, are public, and are curated by
people who are not us. Using them means the target selection is not ours to bias, and
that this dataset is comparable with others built on the same lists.

**Never:**

- Range scanning, host enumeration, port sweeps, or any form of discovery.
- Any host not in the pinned lists and not ours.
- Content that is illegal to access. The published lists are the boundary; adding a
  target outside them is a decision requiring explicit approval, and is recorded in
  `docs/CHANGELOG-lists.md`.

**Load.** One request per target per probe per six hours, at most twelve concurrent
requests, jittered. Less traffic than one person opening the page. Test-list sites are
frequently small, under-resourced, and already scanned by everyone in this field;
adding measurable load to them would be both rude and self-defeating.

**Identification.** The agent sends an ordinary browser User-Agent, because a
distinctive one would bias the measurement — see `METHODOLOGY.md`, section 5. The
project identifies itself instead by being public: this repository, the contact
address below, and the operator's own registration data on the probe addresses. Any
operator who wants a probe to stop contacting them can have that, immediately, by
writing to the contact address; the exclusion is recorded in the repository so the
gap in the data is visible and explained rather than silent.

---

## 3. Data collected, and data deliberately not collected

**From each measurement, stored:** target URL and hostname, resolved addresses,
connection stage, verdict, kernel error, HTTP status, timings, TLS parameters and
certificate fingerprint, response length, SHA-256 of the first 64 KiB of the body,
and the `<title>` element.

**Not stored:** response bodies. The hash and title are enough to recognise a block
page and to detect that two vantage points received different content for the same
URL. Keeping the content itself would mean this dataset carried third-party material
for no analytical gain.

**From each probe, stored:** probe id, network type, autonomous system, country,
region.

**Not stored: the probe's own address.** This matters for the residential probe,
whose address identifies a household. The address is held in a local cache file on the
probe so that a change can be detected, that file is never shipped, and what reaches
the dataset is the ASN and an `asn_changed` event. The validator rejects any row
carrying a `probe_ip` field, so this cannot be undone by accident.

**No personal data of any kind is collected**, from anyone, at any point. The project
measures paths between machines it operates and public websites. It has no users, no
accounts, no telemetry, and no third-party subjects.

---

## 4. Third-party traffic

No probe captures traffic that is not its own. Packet captures introduced in Tier 2
are taken on interfaces of machines the project operates, filtered to the project's
own flows, and truncated (`-s 128`) so that payloads are not retained. Captures are
never committed to the repository and never published; the published artefacts are
derived data and code.

The **responder** is reachable by anyone who finds it. It therefore records full
connection detail only for connections presenting the project token. Every other
connection increments a per-port counter and is otherwise discarded — no address, no
timestamp, no request. This is enforced in the code, not in policy.

---

## 5. Probes on machines the project does not own

Not currently applicable — every probe is operated by the project owner. The rules
are stated now because the moment a volunteer offers a vantage point is the wrong
moment to start writing them.

A probe may run on someone else's machine only when all of the following hold:

- The operator has given explicit, informed, written consent, and understands that the
  machine will contact sites on the Citizen Lab lists.
- The consent record is on file in `docs/consent/` (git-ignored) and the registry
  entry for that probe carries `consent: recorded`.
- The operator can withdraw at any time, for any reason, without explanation, and the
  probe stops the same day. Withdrawal removes future collection; the operator may
  also request removal of past data, and that request is honoured.
- The published data identifies the probe by ASN and region only, never by address,
  city-level precision, or anything that identifies the person.

`internal/registry` refuses to load a registry in which an *active* probe lacks a
consent value. An unconsented vantage point cannot be marked active, so it cannot
produce valid rows.

Risk to the operator is stated plainly rather than minimised: the machine will fetch
sites from a public research list, some of which are blocked in Russia, and this is
visible to their internet provider. That is the risk they are consenting to.

---

## 6. Publication

- The dataset is published under an open licence with the methodology and this
  statement attached. Failures, downtime and gaps are published with it: a dataset
  that reports only the runs that worked is not a measurement, it is an argument.
- Corrections are published as new versions with a changelog, never by silently
  rewriting released data. Files are checksummed at the probe before shipping and the
  checksum is verified on arrival; a file whose checksum does not match is rejected
  rather than admitted.
- Limitations are published as prominently as results. `METHODOLOGY.md` section 10 is
  part of the deliverable, not an appendix.

---

## 7. Contact

Questions, objections, and requests for a site to be excluded from measurement should
go to the address published in the repository README. Requests to stop measuring a
host are honoured without argument and recorded in the repository.
