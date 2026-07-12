# ADR 0016 — OpenTofu Actuator: plan/apply behind Gates, encrypted HTTP state backend

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5, §1.8, §2.3 (Actuator), §3 (OpenTofu over Terraform), §8 (Phase 2)

## Context

Phase 2's second slice. The composing machinery exists: Workflows + Gates give the
plan → human Gate → apply shape (ADR-0011); Contracts v1 gives the params seam
(ADR-0015); the dispatcher supports per-Step images. OpenTofu is charter-named —
no scout needed for the tool itself; the pinned release is checksum-verified in the
EE build.

## Decision

1. **EE image** (`ee/tofu.Dockerfile`): `python3-alpine` + OpenTofu **v1.12.3**
   (sha256-verified against the release manifest, §7.3), non-root runner uid 1000,
   same `/runner` contract as the ansible EE. Python hosts the event driver
   (ADR-0002: Python in execution pods only).
2. **Encrypted HTTP state backend** (`core/internal/statebackend`, migration 00007):
   tofu's http backend protocol (GET/POST + LOCK/UNLOCK, lock-ID-checked writes →
   423 with the holder document). State is **AES-256-GCM encrypted before it touches
   the store** (`STRATT_STATE_KEY`, 32-byte hex); the `graph.opentofu_state.data`
   column holds ciphertext only. Mounted at `/statebackend/` outside `/api/v1` —
   pods are not Principals. **Pod auth:** per-workspace basic-auth credential
   `hex(HMAC-SHA256(key, workspace))` — stateless to verify, scoped to one
   workspace, delivered via `TF_HTTP_PASSWORD` env (`JobSpec` gained an `Env` field;
   credentials never ride ConfigMap files). Recorded dev-grade; hardening
   (per-Run nonce credentials, TLS) is a follow-up.
3. **Actuator** (`core/internal/actuators/opentofu`): params Contract
   `actuators/opentofu.input` (module, mode plan|apply, workspace, vars, eeImage —
   rung 1). Prepare renders the module + http-backend config + tfvars + a python
   driver wrapping every `tofu -json` line as `{counter, tofu}` (deterministic Seq
   → server-side dedup across retries). **No state key ⇒ the actuator is not
   registered and Prepare refuses — never silent plaintext local state.**
4. **Targets:** tofu executes once per Run; the single logical target is the
   workspace. The Run's View remains the audit/blast-radius anchor; ansible-style
   per-target fan-out doesn't apply to a state-graph tool.
5. **Interpret:** `planned_change`, `change_summary` (add/change/remove),
   `diagnostic` (severity + summary + detail — §1.8), `apply_start/complete`,
   `outputs`, `resource_drift` lift to event kinds; the full `tofu show -json` plan
   rides one `plan-json` event (small modules in v1 — S3 artifact offload is the
   recorded follow-up for large plans). Terminal: plan-ok → `ok`, apply-ok →
   `changed`, rc≠0 → `failed`.
6. **The Gate shape:** plan and apply are two Steps of one Workflow sharing a
   workspace; the human Gate sits between them (slice-7 machinery, zero changes).
   **v1 apply re-plans** against the same state rather than applying a stored plan
   artifact — the drift window between the reviewed plan and the apply is
   documented here, not hidden; exact-plan apply arrives with S3 artifact storage.

## Consequences

- Provision-tool state never exists unencrypted at rest inside the platform, and
  never locally in pods.
- `tofu plan` Runs are cheap, read-only, and streamable — the future drift-detection
  Baseline ("tofu plan on cron *is* drift detection") needs only a Trigger.
- Deferred (next slice): rung-2 output-derived Contracts from `tofu output`,
  outputs → Entity projection via a Normalizer, cross-Step output binding — the
  provision→configure seam.
- Follow-ups: state-backend hardening; S3 plan artifacts + exact-plan apply;
  `destroy` mode (deliberately absent in v1 — the most dangerous verb arrives with
  its own review); PlanDiff UI consuming `plan-json`/`change_summary` (ADR-0003 L3).
