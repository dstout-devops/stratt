# ADR 0029 — Evidence store (object-locked audit bundles)

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-3 "Evidence store (object-lock)"), §2.4
  (Evidence — immutable object-locked artifact bundle; the audit/PCI export
  unit), §1.8 (never hide/fake failure), §1.2 (projections), §2.5 (secrets),
  §1.6 (agent-native), §1.4 (boring spine), §3 (S3-compatible object store);
  ADR-0019 (Findings), ADR-0027 (finding-open hook)

## Context

Phase 3 turns the Findings shipped in Phase 2 into the charter's **Evidence**
Named Kind (§2.4). Previously "Evidence" was only `Finding.RunID` + the redacted
`Finding.diff`; there was **no object storage anywhere**. When a Finding opens,
its supporting record — the drift diff, the check Run, and (critically) the Run's
NATS task-event stream, which lives only 14 days and is otherwise lost — must be
**sealed** so it survives as the audit/PCI unit. This is the compliance backbone
the Phase-3 "security review" promote gate needs.

## Decision

1. **Client + substrate (§1.4).** Reuse the already-vendored `aws-sdk-go-v2`
   family — add only `service/s3` (full object-lock primitives). Dev store:
   **SeaweedFS** (Apache-2.0, S3-compatible), added to the compose. Rejected
   `minio-go` (redundant; "never MinIO-by-name" optics) and, for the dev store,
   Garage (AGPL + no object lock) and moto (store-and-ignore lock).
2. **The immutability model is DEFENSE-IN-DEPTH — and honestly scoped (§1.8).**
   The empirical finding drove this: **no charter-compatible Apache-2.0 dev S3
   backend enforces object-lock WORM** — moto and SeaweedFS (3.97 and 4.39) all
   accept the lock config + retention and then ignore it (overwrite *and* delete
   succeed, both GOVERNANCE and COMPLIANCE, verified with a throwaway probe);
   Garage has no object lock; MinIO enforces but is charter-banned; real AWS S3
   enforces but isn't local. So immutability is three layers, and we **do not
   claim WORM the dev backend does not enforce**:
   - **object-lock retention config** is applied to every sealed object
     (`ObjectLockMode` + `RetainUntilDate`); a compliant production store
     (AWS S3 Object Lock) enforces this as WORM at the storage layer.
   - **sha256 tamper-evidence** — backend-independent and the load-bearing dev
     guarantee: the content hash is recorded in the manifest, and **every read
     re-hashes the object and refuses a mismatch** (`GetVerified` → `ErrTampered`).
     A mutated Evidence object is *detected and never served as authentic*,
     regardless of backend. (Tamper-**evident**, not tamper-**proof**, in dev.)
   - **write-once by construction** — the platform never overwrites or deletes a
     sealed object; the `graph.evidence` unique-per-Finding index enforces one
     manifest, so re-sealing is a no-op.
3. **Manifest, not a second truth (§1.2).** The immutable bundle lives in the
   object store; `types.Evidence` / `graph.evidence` (migration 00016) is the
   graph's **pointer** to it (object_key + sha256 + retain_until), a rebuildable
   projection — not a copy.
4. **Seal at Finding-open, retry-safe (§4.3).** After each baseline evaluation,
   the `SealEvidence` activity seals a bundle for every **open Finding lacking a
   manifest** (`ListUnsealedFindings`) — keyed by the manifest's absence, not the
   one-shot pending→open transition, so a seal failure + activity retry re-seals
   the missed Findings instead of losing them. The bundle: redacted diff +
   Finding metadata + (for check-Run Findings) the Run summary and a durable
   snapshot of the Run's NATS task-event stream. Gated on an object store being
   configured (`STRATT_EVIDENCE_BUCKET`); absent → Findings open unsealed (a
   logged no-op), like the opentofu actuator is gated on a state key.
5. **Agent-native reads (§1.6).** `GET /findings/{id}/evidence` (manifest) and
   `GET /evidence/{id}/download` (streams the bundle after re-verifying sha256;
   tampered → 409); a `get_finding_evidence` MCP tool; a UI download link.
6. **§2.5:** object-store credentials via the SDK env chain, never persisted.

## Consequences

- **Live-verified (dev harness, SeaweedFS + real substrate):** a `fleet-drift`
  facet baseline opened **51 Findings → 51 Evidence bundles sealed** (distinct
  objects + sha256, object-lock retain-until 2027); `GET /findings/{id}/evidence`
  returns the manifest and `GET /evidence/{id}/download` streams the verified
  bundle (200 + `X-Stratt-Evidence-SHA256`). **The tamper→409 path is proven end
  to end:** mutating a sealed object directly in SeaweedFS makes the download
  return `409 "evidence object failed its integrity check"` — the honest
  immutability guarantee working even without backend WORM. A store-layer
  integration test proves write-once (a re-seal conflicts) and the unsealed
  work-list. The `evidencestore` tamper unit test proves the same at the client.
- **Documented dev-backend gap:** SeaweedFS does not enforce object-lock (verified
  empirically); in dev the guarantee is tamper-evidence + write-once + applied
  config; **true WORM enforcement requires a compliant production object store**
  (AWS S3 Object Lock). This is stated, never hidden (§1.8).
- **Scope note:** the event-stream snapshot in the bundle is populated only for
  check-Run Findings (they carry a `RunID`); facet-observation Findings evaluate
  graph-side with no Run, so their bundle is the diff + metadata (correct by
  design — the Temporal history is their evidence).

## Deferred / fast-follow (documented)
- Bucket-side SSE encryption of bundles (v1 = object-lock config + sha256
  integrity + write-once; confidentiality-at-rest is orthogonal).
- A production Helm slice pinning a compliant object store (AWS S3 / on-prem WORM)
  so object-lock is enforced, not just configured — plus a startup probe that
  fails loudly if the configured backend does not enforce retention.
- Evidence for orphan Findings (Intent-withdrawal) and tofu-plan Baselines.
- reader-gated Evidence reads (ties to the ADR-0028 `view.reader` relation; v1
  reads are authenticated like Findings); presigned download URLs; bulk PCI export.

## Runway after
Phase-3 board continues: Sites (NATS leaf) + pull agent/Bundles; Intent/Certificate
GA (the promote flagship); audit→Splunk; HA/DR; SCIM.
