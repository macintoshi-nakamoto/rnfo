# Data schema

Current version: **v1**. The authoritative definition is
[`internal/schema/schema.go`](../internal/schema/schema.go); this file explains it.

## Versioning rule

The meaning of a field never changes silently.

- Adding a new **optional** field: allowed within the same version, documented here.
- Changing what an existing field means, changing its type, or removing a required
  field: **bump the version** and describe the change below.
- The validator rejects rows whose `schema` it does not know, so an old consumer can
  never silently misread new data.

## Files

Written by the probe under `RNFO_DATA_DIR`:

```
data/measurements/YYYY-MM-DD.jsonl      one row per target per attempt
data/measurements/YYYY-MM-DD.jsonl.sha256
data/runs/YYYY-MM-DD.jsonl              run and event records
data/runs/YYYY-MM-DD.jsonl.sha256
```

Finished days are sealed with a SHA-256 sidecar by the next run. Today's file is never
sealed because it is still growing. The collector refuses any file whose checksum does
not match, so an admitted day is provably the day the probe wrote.

Archived by the collector under `data/<probe-id>/…` with the sidecars preserved.

`rnfo-collect pull -live` additionally fetches today's still-growing file as
`live-YYYY-MM-DD.jsonl`, with no checksum. Those files are provisional: they are
overwritten on every pull and are replaced by the verified copy once the day is
sealed. Never cite a number from a `live-` file as final.

---

## Measurement record

One target, from one probe, at one moment.

### Identity

| Field | Type | Notes |
|---|---|---|
| `schema` | string | `v1` |
| `run_id` | string | `2026-09-04T18:00Z/full` - the scheduled slot, **identical across probes**, so a Russian row and its foreign control join on `(run_id, url)` |
| `ts` | string | RFC 3339, UTC, milliseconds: `2026-09-04T20:07:33.412Z` |
| `probe` | string | must exist in `probes.yaml` |
| `net` | string | `hosting`, `eyeball`, `mobile` - must agree with the registry |
| `asn` | string | as observed, e.g. `AS203273 NetCrafters OU`. **Group by the leading AS number.** The operator name after it is a label whose spelling depends on which lookup answered (Team Cymru's DNS service or an HTTPS provider); rows written by agent 0.4.0 and 0.4.1 carry Cymru's raw form (`AS8402 CORBINA-AS - PJSC _Vimpelcom_, RU`), later agents normalise it |
| `country`, `region` | string | probe location; never an address |
| `agent` | string | `rnfo-probe/0.1.0` |
| `profile` | string | `full`, `controls`, or `sni` |

### Target

| Field | Type | Notes |
|---|---|---|
| `list` | string | `citizenlab-global`, `citizenlab-ru`, `controls`, `own` (our responder, one row per port), `sni` (the same-address-different-name experiment) |
| `category` | string | Citizen Lab category code, e.g. `NEWS`, `HUMR`. Controls carry `CTRL-RU` or `CTRL-INTL` (agent 0.2.0+; `CTRL` before). Own targets carry `OWN-RESPONDER`. `sni` rows carry the trial class: `own`, `none`, `neutral`, `blocked_observed` |
| `target` | string | hostname |
| `url` | string | URL as it appears in the list |
| `attempt` | int | `1` first try, `2` confirmation retry after a failure. In `sni` rows this is the round number, 1–5 |

### Resolution

| Field | Type | Notes |
|---|---|---|
| `dns_rc` | int | `0` ok, `1` NXDOMAIN, `2` timeout, `3` other |
| `dns_err` | string | resolver error, when there was one |
| `dns_ips` | []string | **all** addresses returned, both families - a poisoned answer is visible here even when the connection then succeeds |
| `remote_ip` | string | the address actually dialled |
| `ip_family` | string | `v4` or `v6`; fixed per study, see `METHODOLOGY.md` §5 |

### Outcome

| Field | Type | Notes |
|---|---|---|
| `stage` | string | `ok`, `dns`, `tcp`, `tls`, `request`, `response` |
| `verdict` | string | closed vocabulary, see below |
| `curl_rc` | int | compatibility mapping onto curl exit codes; derived from `verdict`, and the validator rejects rows where the two disagree |
| `err` | string | error text |
| `errno` | string | `ECONNRESET`, `ETIMEDOUT`, `ECONNREFUSED`, `EHOSTUNREACH`, `ENETUNREACH`, `EPIPE` |

**Verdicts and their curl equivalents.** `stage` says where, `verdict` says where and
how, `curl_rc` is what a curl-based tool would have reported.

| verdict | stage | curl_rc | what the network did |
|---|---|---|---|
| `ok` | ok | 0 | response received |
| `dns_nxdomain` | dns | 6 | name does not resolve |
| `dns_timeout` | dns | 6 | resolver did not answer |
| `dns_fail` | dns | 6 | resolution failed otherwise |
| `no_address_family` | dns | 6 | host has no address of the family being measured - **not a block** |
| `connect_refused` | tcp | 7 | RST at connect, or nothing listening |
| `connect_timeout` | tcp | 28 | SYN silently dropped |
| `connect_unreachable` | tcp | 7 | no route |
| `connect_reset` | tcp | 56 | reset during the TCP handshake |
| `tls_reset` | tls | 35 | reset after the ClientHello - consistent with name-based inspection |
| `tls_timeout` | tls | 35 | handshake packets silently dropped |
| `tls_cert_name` | tls | 60 | certificate does not match the requested name |
| `tls_cert_invalid` | tls | 60 | certificate otherwise invalid |
| `tls_fail` | tls | 35 | handshake failed otherwise |
| `request_reset` | request | 56 | reset while sending the request |
| `request_timeout` | request | 28 | no response headers |
| `response_reset` | response | 56 | reset while receiving the body |
| `response_timeout` | response | 28 | body stalled |
| `response_truncated` | response | 56 | body ended early |
| `response_empty` | response | 52 | headers but no body |
| `too_many_redirects` | request | 47 | redirect limit reached |
| `other` | any | 2 | unclassified |

### HTTP

| Field | Type | Notes |
|---|---|---|
| `http` | int | status code; `0` when no response arrived |
| `http_version` | string | e.g. `HTTP/1.1` |
| `redirects` | int | redirects followed, limit 5 |
| `final_url` | string | after redirects |
| `server` | string | `Server` header, truncated |

### Timings

Seconds, floating point. **Deltas, not cumulative** - this differs from curl's
`time_connect`/`time_appconnect`, which are cumulative from the start of the request.

| Field | Meaning |
|---|---|
| `t_dns` | resolution |
| `t_connect` | TCP handshake alone |
| `t_tls` | TLS handshake alone |
| `t_firstbyte` | request sent to first response byte |
| `t_total` | whole measurement |

### TLS

| Field | Notes |
|---|---|
| `tls_version`, `tls_cipher`, `tls_alpn` | negotiated parameters |
| `cert_subject`, `cert_issuer`, `cert_not_after` | leaf certificate |
| `cert_sha256` | leaf fingerprint - compare across probes in the same slot |
| `cert_name_ok` | whether the leaf matches the requested name |

Reading `cert_name_ok: false` correctly matters. On a row whose verdict is `ok`, it
usually means the site is misconfigured, not that anything intercepted the connection.
Interception evidence needs an unexpected issuer, or a fingerprint that differs from
the foreign control's for the same host in the same slot.

### Body and wire counters

| Field | Notes |
|---|---|
| `body_len` | bytes of body read, capped at 2 MiB |
| `body_sha256` | SHA-256 of the first 64 KiB |
| `body_truncated` | true when the 2 MiB cap was hit |
| `title` | `<title>`, whitespace-collapsed, 200 runes |
| `bytes_read` | **wire** bytes received before the connection ended |
| `bytes_sent` | wire bytes sent |

`bytes_read` is the field that makes volume-triggered blocking measurable: when a
connection is cut mid-transfer, this is how much arrived first.

---

## Run record

Written once per run into `data/runs/`. Distinguished by `"kind": "run"`.

| Field | Notes |
|---|---|
| `run_id`, `probe`, `net`, `asn`, `country`, `region`, `agent`, `profile` | as above |
| `started_at`, `finished_at`, `duration_s` | wall clock |
| `targets`, `rows`, `ok`, `failed` | counts; `rows` exceeds `targets` when retries ran |
| `by_verdict` | verdict histogram |
| `controls_total`, `controls_ok` | all connectivity controls, domestic and international |
| `controls_intl_total`, `controls_intl_ok` | the international subset (agent 0.2.0+) |
| `healthy` | **agent 0.2.0+:** false when half or more of the *international* controls failed. **agent 0.1.0:** judged on all controls. Either way: **analysis must discard the run's measurements**, the probe had no usable network. The `agent` field says which rule applied. From 0.4.3 this flag also gates the confirmation retry: a healthy run with failures always has `attempt: 2` rows; before 0.4.3 the retry was skipped when more than 40 % of the run failed, whatever the controls said |
| `list_manifest` | list name → SHA-256 of the file used, so any row traces to its exact target set |
| `resolver` | (agent 0.3.0+) DNS server used when it was not the system resolver - set on Termux hosts, where it is the home router; empty on servers. A run using a public resolver measures something different, and this says so |
| `max_body` | (agent 0.3.0+) the per-target body cap the run was configured with; lowered on metered links. `body_len`/`body_truncated` are read against it |
| `clock_offset_ms`, `clock_source` | (agent 0.4.0+) this host's clock minus network time from one SNTP exchange per run, and the server that answered. Absent when the check failed. Pairing across probes is a join on timestamps, so this is data quality, not housekeeping |

The run record is what makes downtime data rather than absence. A slot with no
measurements *and* no run record means the probe was down. A slot with a run record
and few rows means something else, and the record says what.

---

## Event record

Written into `data/runs/`. Distinguished by `"kind": "event"`.

| `type` | Meaning |
|---|---|
| `asn_changed` | the uplink moved to a different autonomous system - expected on a dynamic residential line, and it invalidates comparisons across the boundary |
| `agent_started` | (0.3.0+) the daemon started; on a handset this marks a reboot or an Android kill-and-restart |
| `run_skipped_overlap` | (0.3.0+) the daemon skipped a slot because the previous run of that profile was still going, as systemd would refuse a second instance |
| `clock_offset` | (0.4.0+) the clock was more than one second from network time; timestamps in that run carry that error |
| `resolver_fallback` | (0.4.1+) the configured resolver did not answer at start and a public one is in use; `from` is the configured one, `to` the one used. DNS behaviour in such runs is that of the fallback resolver |
| `identity_unknown` | both geolocation providers were unreachable; the run continued on a cached identity up to three hours old |

---

## Change log

| Version | Date | Change |
|---|---|---|
| v1 | 2026-09-04 | Initial schema. |
| v1 (agent 0.4.3) | 2026-09-08 | No field meaning changed. The confirmation retry is gated on `healthy` instead of on a 40 % failure ceiling; the ceiling had silently disabled retries on the residential probe, whose normal failure rate is 40-41 %. Rows from `ru-mow-home` before 2026-09-08T18:00Z have no `attempt: 2`; see METHODOLOGY §4.3 and limitation 10. |
| v1 (agent 0.4.0) | 2026-09-05 | No field meaning changed. Run record gains optional `clock_offset_ms` and `clock_source`; new event `clock_offset`. Identity is now found by DNS first (a "myip" resolver plus Team Cymru's ASN service), with the HTTPS providers only adding region and city, so a probe on a network that dislikes API hosts, or a handset without a CA store, still names its ASN. `asn_changed` compares AS numbers only, because the two lookup paths spell operator names differently. |
| v1 (agent 0.3.0) | 2026-09-05 | No field meaning changed. Run record gains optional `resolver` and `max_body`. New event types `agent_started`, `run_skipped_overlap`. Daemon mode for hosts without systemd produces identical records. |
| v1 (agent 0.2.0) | 2026-09-04 | No field meaning changed. Added `controls_intl_total`/`controls_intl_ok`; `healthy` is now computed from international controls only, because a domestic Russian control (`gosuslugi.ru`) is not obliged to answer a foreign probe and did not. Control categories became `CTRL-RU`/`CTRL-INTL`. New `list` values `own` and `sni`, new `profile` value `sni`. |
