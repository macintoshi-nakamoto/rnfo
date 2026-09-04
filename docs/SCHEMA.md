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

---

## Measurement record

One target, from one probe, at one moment.

### Identity

| Field | Type | Notes |
|---|---|---|
| `schema` | string | `v1` |
| `run_id` | string | `2026-09-04T18:00Z/full` — the scheduled slot, **identical across probes**, so a Russian row and its foreign control join on `(run_id, url)` |
| `ts` | string | RFC 3339, UTC, milliseconds: `2026-09-04T20:07:33.412Z` |
| `probe` | string | must exist in `probes.yaml` |
| `net` | string | `hosting`, `eyeball`, `mobile` — must agree with the registry |
| `asn` | string | as observed, e.g. `AS203273 NetCrafters OU` |
| `country`, `region` | string | probe location; never an address |
| `agent` | string | `rnfo-probe/0.1.0` |
| `profile` | string | `full` or `controls` |

### Target

| Field | Type | Notes |
|---|---|---|
| `list` | string | `citizenlab-global`, `citizenlab-ru`, `controls`, `own` |
| `category` | string | Citizen Lab category code, e.g. `NEWS`, `HUMR`; `CTRL` for controls |
| `target` | string | hostname |
| `url` | string | URL as it appears in the list |
| `attempt` | int | `1` first try, `2` confirmation retry after a failure |

### Resolution

| Field | Type | Notes |
|---|---|---|
| `dns_rc` | int | `0` ok, `1` NXDOMAIN, `2` timeout, `3` other |
| `dns_err` | string | resolver error, when there was one |
| `dns_ips` | []string | **all** addresses returned, both families — a poisoned answer is visible here even when the connection then succeeds |
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
| `no_address_family` | dns | 6 | host has no address of the family being measured — **not a block** |
| `connect_refused` | tcp | 7 | RST at connect, or nothing listening |
| `connect_timeout` | tcp | 28 | SYN silently dropped |
| `connect_unreachable` | tcp | 7 | no route |
| `connect_reset` | tcp | 56 | reset during the TCP handshake |
| `tls_reset` | tls | 35 | reset after the ClientHello — consistent with name-based inspection |
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

Seconds, floating point. **Deltas, not cumulative** — this differs from curl's
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
| `cert_sha256` | leaf fingerprint — compare across probes in the same slot |
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
| `controls_total`, `controls_ok` | connectivity control outcome |
| `healthy` | false when half or more of the controls failed — **analysis must discard the run's measurements**, the probe was off the network |
| `list_manifest` | list name → SHA-256 of the file used, so any row traces to its exact target set |

The run record is what makes downtime data rather than absence. A slot with no
measurements *and* no run record means the probe was down. A slot with a run record
and few rows means something else, and the record says what.

---

## Event record

Written into `data/runs/`. Distinguished by `"kind": "event"`.

| `type` | Meaning |
|---|---|
| `asn_changed` | the uplink moved to a different autonomous system — expected on a dynamic residential line, and it invalidates comparisons across the boundary |
| `identity_unknown` | both geolocation providers were unreachable; the run continued on a cached identity up to three hours old |

---

## Change log

| Version | Date | Change |
|---|---|---|
| v1 | 2026-09-04 | Initial schema. |
