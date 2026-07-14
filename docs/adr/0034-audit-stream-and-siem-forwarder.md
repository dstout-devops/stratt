# ADR 0034 — The one audit stream + vendor-neutral SIEM forwarder

- **Status:** Accepted (Commit 1 — the audit ledger; Commit 2 — the forwarder + drivers)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.6 ("one Principal model, one authorization model, **one audit
  stream**, cost/usage accounting per identity"), §1.2 (projections vs born-here operational
  records), §2.5 (CredentialRef — material only in pods at spawn), §1.8 (never hide failure),
  §1.4/§1.5 (boring spine; pinned/sovereign — no external protocol load-bearing); ADR-0021
  (audit.mcp_call, the born-here-records precedent), ADR-0027 (the notify §2.5 credential-in-pod
  delivery pattern this reuses), ADR-0028 (View-scoped authz — the audit:log gate mirrors it),
  ADR-0029 (Evidence sha256 tamper-evidence — the seal-after pattern this mirrors)

## Context

The charter promises "one audit stream" (§1.6). It did not exist. `audit.mcp_call` recorded
only MCP tool calls (aggregate-only reader); everything else was scattered — `graph.run` had
**no principal column**, gate decisions used `decided_by`, Provenance stamps a *writer* not a
*Principal*, and **authz allow/deny decisions were persisted nowhere**. So "audit→Splunk" was
two problems: there was no unified audit event to forward. The steward framed it as a statement
feature — the complete, tamper-evident audit trail is the prize AAP/Splunk gate behind license;
Stratt makes the *ledger itself* an open capability, then forwards it without baking a vendor.

Steward chose the maximal shape: hash-chained + a verify endpoint; **full access log** (reads
included); three SIEM drivers (Splunk HEC + syslog + OTel-logs); a **long-lived forwarder pod**.

The notify Sink/webhook plane (ADR-0027) is the *wrong delivery model* for audit — pod-per-event,
CEL-filtered subset, cooldown **drops**, no ordering/completeness/DLQ, 7-day retention. But its
**§2.5 credential-in-pod pattern** (resolve a CredentialRef to a mount pointer; the pod reads
`/runner/credentials/…`; sanitized failures) is exactly reusable. Audit is a **born-here
operational record** (the `audit` schema, §1.2 — the graph stays projection+provenance).

## Decision (Commit 1 — the audit ledger)

1. **`audit.event` (migration 00019): append-only, ordered, hash-chained.** Monotonic
   `seq bigserial`, `principal_id`+`kind`, `action`, `object`, `outcome`, `detail`, and the
   tamper-evidence chain (`prev_hash`/`hash`). Append-only + seal-once is enforced by a DB
   trigger (structural, §1.2 ethos): DELETE refused, a row UPDATEable only to seal it, a sealed
   row immutable — tampering requires elevated DB rights, which the hash chain still detects.

2. **Tamper-evidence, decoupled from the hot path.** `core/internal/audit` computes
   `hash = sha256(prev_hash || canonical(event))`. A single-writer **Sealer** (a controller in
   strattd, twin of the baseline/trigger reconcilers) chains the unsealed tail in `seq` order
   ~1 s — so integrity never bottlenecks the full access log (mirrors Evidence seal-after,
   ADR-0029). `GET /audit/verify` walks the sealed chain and reports the **first** break:
   altered content (hash mismatch), a broken link (a removed middle row), or a truncated tail
   (the sealed rows don't reach the seal head).

3. **Emit surface: one chokepoint + rich domain events.** An HTTP audit middleware behind the
   Principal resolver records **every authenticated request** (the full access log). The events
   the HTTP layer can't see get explicit emits from the Temporal activities that run on *every*
   run path — **authz exec-grant allow/deny** and **credential.use** (previously logged nowhere)
   — plus **MCP tool calls** folded in (`GET /usage` re-sourced over the one stream).

4. **Query API, deny-by-default.** `GET /audit` (cursor-paged by seq) + `GET /audit/verify`,
   gated behind a **reader grant on a new `audit:log` authz object** (audit reads are privileged,
   unlike v1's open GETs); `get_audit`/`verify_audit` MCP tools mirror them.

## Decision (Commit 2 — the vendor-neutral forwarder)

5. **`Sink` generalized to the outbound-destination noun** (already vocabulary-blessed, ADR-0027):
   kinds `splunk-hec`/`syslog`/`otel-logs` alongside `webhook`, with SIEM config (endpoint,
   index, facility, insecure). The notifier still consumes `webhook`; the forwarder consumes SIEM
   kinds. **No Subscription for audit** — audit ships *everything* (completeness). "Sink"/
   "forwarder" stay delivery-plane nouns, never §2 Named Kinds.

6. **Server-owned cursor, at-least-once, egress never-drop.** `audit.forward_offset` per sink;
   `GET /audit/forward/{sink}` returns the next in-order batch after the committed offset;
   `POST …/report` commits **only on `delivered`** (forward-only, `GREATEST`) and records the
   outcome to `audit.forward_delivery` (§1.8), while a `failed` report records the failure but
   **never advances** — the batch re-ships until it lands. **Egress cannot drop a record.** (This
   never-drop guarantee is for *egress*; see the deferrals for the best-effort *ingest* emit.)
   The forward endpoints (batch/report/config) are gated by a distinct `forwarder` relation on
   `audit:log`, separate from the `reader` grant that guards `GET /audit`/`verify` — a read-only
   audit grant cannot advance a cursor (least-privilege, §1.6; charter-guardian Flag 2).

7. **`stratt-forwarder`: a long-lived pod, §2.5-clean.** It reads batches from the platform API
   as its own Principal (must hold `audit:log` forwarder), ships through a vendor-neutral `Driver`
   seam, and reports. The SIEM credential is injected into the pod at spawn
   (`/runner/credentials/siem/…`); the daemon holds only a pointer. Drivers hand-roll the wire
   formats — Splunk HEC (`/services/collector/event`, `Authorization: Splunk`), RFC 5424 syslog
   (octet-counted over TCP/TLS), OTLP/JSON logs (`/v1/logs`) — so the forwarder is a
   dependency-free static binary and no SIEM protocol is load-bearing in core (§1.4/§1.5,
   "S3-compatible, never MinIO-by-name"). Failures are sanitized (never log endpoint/token, §2.5).

## Charter posture

- **§1.6** one Recorder, one Principal stamp on every action (human/agent/CI/MCP), one query
  surface; `GET /usage` re-sourced from it.
- **§1.2** audit is a born-here operational record in the `audit` schema — the ledger *is* the
  truth of what happened, not a rebuildable projection; Provenance still governs graph writes.
- **§2.5** the SIEM credential is a CredentialRef injected into the forwarder pod; the control
  plane never holds it; drivers sanitize failures.
- **§1.8** tamper-evident hash chain + verify; forward DLQ-as-alarm + delivery status; egress
  never-drop (ingest is best-effort but always surfaces failure loudly — see deferrals).
- **§1.4/§1.5** a `Driver` seam; Splunk/syslog/OTel are drivers beneath it; none load-bearing.

## Alternatives considered

- **Forward the existing records as-is.** There was no unified, Principal-stamped, ordered event
  to forward — the ledger had to be built first. That build is the larger value.
- **Reuse the notify Sink/webhook delivery plane.** Pod-per-event, filtered, cooldown-drops, no
  ordering/DLQ — categorically wrong for a compliance audit trail. Reused only the §2.5 pattern.
- **Synchronous hash chaining on append.** Serializes every audited request (the full access log)
  behind the chain lock. Rejected for the decoupled sealer (integrity ≠ throughput bottleneck).
- **Cursor-driven batched Jobs vs a long-lived forwarder.** Steward chose the long-lived pod for
  continuous streaming without pod churn; both keep the credential in a pod (§2.5).
- **An OTel-SDK OTLP exporter.** Hand-rolling OTLP/JSON keeps the forwarder dependency-free (no
  new module) — dependency-scout no-op.

## Honest deferrals

**Best-effort ingest emit (charter-guardian Flag 1).** The *egress* guarantee above is
never-drop, but the *ingest* emit is best-effort: the HTTP middleware records after the response
on a detached context, and the domain emits (exec-grant, credential.use) are best-effort too — if
the DB is unavailable the action already succeeded and the audit event is lost. This is surfaced
loudly (`slog.Error`), so §1.8 "never hide failure" holds, but true never-drop ingest (an outbox /
NOT-NULL-on-commit path for the decision emits) is deferred. `principal_kind` is not threaded
through `RunInput`, so the exec-grant/credential.use emits carry only `principal_id` (Flag 3 —
the load-bearing field is present; threading kind is a deferred completeness nit). A read-holder of
`audit:log` still cannot advance a cursor (Flag 2 fixed via the `forwarder` relation), but a
`forwarder`-grant holder advancing past unshipped events would create a bounded egress gap (ledger
intact, `verify` still passes) — a dedicated per-batch attestation is the hardening.

Other deferrals: the async-seal window (the recent unsealed tail — ~1 s; per-event signing is the harder path);
external anchoring of the chain head (Rekor/notarization) for third-party non-repudiation;
per-Principal audit-read scoping (v1 = one `audit-reader` grant; the same grant lets a reader
advance a forward offset — a dedicated forwarder relation is the hardening); multi-sink fan-out in
one forwarder pod (v1 = one pod per sink); the forwarder authenticating via the API for the audit
read while the daemon computes batches; a UI audit viewer; audit-event schema versioning; per-field
redaction of `detail`; long-SIEM-outage back-pressure/DLQ retention policy. The live kind e2e
(spread of actions → verify OK → tamper → verify fails → forwarder ships → SIEM-down retries →
resume) is harness-wired (`task image:forwarder`, `dev:forwarder:demo`) but not run this session;
the drivers + loop are proven by unit tests against mock SIEM + platform servers.

## Consequences

"One audit stream" becomes real and provable: a complete, ordered, Principal-stamped,
tamper-evident ledger with a verify endpoint, forwardable to any SIEM through a neutral seam — an
open capability where AAP/Splunk gate far less behind license. Audit reads are deny-by-default.
The forwarder is a small static binary reusing the boring spine; adding a driver is a new file
behind the `Driver` interface, not a core change.
