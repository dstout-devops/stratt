# ADR 0030 — `Intent/Certificate` GA (certificate lifecycle as first tenant)

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-3 promote gate — "production for a bounded
  service class (certificate lifecycle as first tenant)"), §2.4 (Intent kinds,
  the intent→Baseline→Finding→remediation loop), §1.1 (type the seams), §1.2
  (projections, never a second truth), §1.5 (sovereign contracts, multiple
  transports), §1.3 (rug-pull-proof), §2.5 (CredentialRef), §5 Flow 2 (remediation
  is never auto-launched), §1.8 (never hide/fake failure); ADR-0023 (compiler),
  ADR-0028 (View-scoped execution), ADR-0029 (Evidence), ADR-0027 (webhook Actuator)

## Context

Phase 3's promote gate is certificate lifecycle. This slice makes
`Intent/Certificate` the second GA payload kind — and the first to drive the
whole intent→finding→remediation spine on a real domain. Before it, only
`Intent/Application` existed, `Intent.Spec` was an untyped `map[string]any` (a
§1.1 gap), no `cert.*` Facets existed, `onRemove: revert|remove` was
unimplemented, and there was no CLM integration.

The CA is an **external system of record reached through a Connector** (§2.4) —
Stratt never implements a CA (that would be a new-config-language / reimplementation
non-goal). We integrate over the CLM's REST API.

## Decision

1. **Dev CLM: OpenBao (§1.3, §1.4).** The certificate authority is **OpenBao**'s
   PKI secrets engine (MPL-2.0, Linux Foundation) — Vault's PKI minus the BUSL-1.1
   relicense that §1.3 ("rug-pull-proof") exists to avoid. **HashiCorp Vault is
   rejected for bundling** (BUSL, the canonical §1.3 rug-pull; admissible only as a
   user-supplied external Connector); step-ca (Apache-2.0) was the scout's lean for
   a cert-only harness, but OpenBao is one governance-safe service covering both the
   CLM *and* a future CredentialRef broker. The `cert-issuer` Connector contract is
   **issuer-agnostic (§1.5)** — step-ca / cert-manager / Vault satisfy it later;
   OpenBao is never load-bearing ("any PKI-compatible CLM, never OpenBao-by-name",
   the ADR-0029 "never MinIO-by-name" pattern). dependency-scout 2026-07-13.
   **Honest scope of "issuer-agnostic" (guardian caveat):** the deterministic core
   treats cert params as opaque, so no transport is load-bearing on it — but the v1
   Actuator + REST client speak the **Vault family** shape (`mount`, `X-Vault-Token`,
   `/v1/<mount>/issue|revoke`). A step-ca / cert-manager issuer plugs in behind the
   same sovereign `cert-issuer` Contract seam (§1.5) but needs its own Actuator; the
   neutrality is the Contract's, not this v1 transport's.
2. **Two Connector halves, each on an existing spine (§1.4).**
   - **Read (Syncer, `core/internal/connectors/certissuer`):** enumerate issued
     certs → parse X.509 (`crypto/x509`, stdlib) → project `cert` Entities with
     `cert.identity` + `cert.expiry` Facets and provenance (mirrors the awsec2
     Syncer; hand-rolled REST client, no new Go dep — §1.5). The CA cert and
     **revoked** certs are skipped, so a revoke/renew reflects in the graph
     (§1.2: the graph mirrors the CLM, never invents). Read-side token via the
     env chain (`STRATT_CLM_TOKEN`), like the other Syncers.
   - **Write (Actuator, `core/internal/actuators/certissuer`):** issue / renew /
     revoke run **inside an execution pod**, the CLM token injected as a
     CredentialRef file at spawn (§2.5) — the webhook-Actuator precedent (ADR-0027).
     **Vocabulary note:** the charter names `revoke-cert` an *Action* (§2.2); with
     no Action-execution framework yet, modeling it as an Actuator is a conscious
     deferral (the webhook precedent), not drift.
3. **Intent-kind machinery GA (§1.1).** `Intent.Spec` is now **typed at its seam**:
   each kind has a JSON-Schema Contract (`contracts/intents/<kind>.schema.json`),
   validated at parse (`ValidateIntent`). A kind is "implemented" iff its schema
   exists; the `Intent/Application`-only gates are gone. The Certificate schema
   makes `exportable: false` a **schema constraint, not a policy memo** (§2.4).
   Reusable for the FileSet/Access kinds.
4. **Baseline-side expiry threshold.** A new `FacetExpectation.notBefore`
   operator asserts an RFC3339 timestamp is at least a Go-duration window in the
   future at evaluation time. The window is **Git policy** — the Intent spec's
   `renewBefore`, substituted into the Blueprint at compile (`{{.spec.renewBefore}}`).
   Minimal, reusable beyond certs.
5. **`onRemove: remove` → Gated revoke (§5 Flow 2, §1.8).** A Certificate Intent
   admits `onRemove: remove`. On Assignment withdrawal the compiler consults the
   still-declared Intent; `remove` surfaces the Blueprint's `removeWorkflow`
   (a revoke Workflow) on the orphan Finding — **a ref the operator launches, never
   auto-run**. Revoke really happens (full lifecycle) with the charter-mandated
   human/Gate in the loop.
6. **Reads reuse the spine (§1.6):** Findings API/UI/MCP, and **Evidence
   auto-seals** on Finding-open (ADR-0029) — a cert drift Finding gets an
   object-locked audit bundle for free.

## Consequences

- **Live-verified (dev harness: OpenBao + kind + EE + substrate) — the full
  lifecycle:** OpenBao issued `web.stratt.test` (720h, healthy) and
  `api.stratt.test` (48h, expiring); the certissuer Syncer projected 2 `cert`
  Entities with cert.identity/cert.expiry + syncer provenance; the Certificate
  Blueprint compiled a facet-observation Baseline whose **threshold opened one
  warning Finding** on the expiring cert ("within renewal window (expires
  2026-07-15)") while the healthy cert stayed clean; **Evidence sealed** (sha256,
  retain 2027). The **cert-renew Workflow** issued a fresh cert via the
  pod-injected CredentialRef — the CLM token **never appeared in strattd logs**
  (§2.5) — and the new cert projected healthy. A **revoke** Run revoked the old
  cert in OpenBao → the Syncer tombstoned it → the graph showed two healthy certs.
  Withdrawing the Assignment (`onRemove: remove`) produced an **orphan Finding
  carrying `removeWorkflow: cert-revoke`**. The renew was correctly **denied**
  until `use` on `credential_ref:cert-issuer` was granted — proving the §2.5
  credential authz check gates the write path.
- **Documented gap (§1.8): a renewed cert changes Entity identity (new serial =
  new Entity), so the pre-renewal drift Finding lingers `open` after its cert is
  revoked/tombstoned** — `RecordBaselineObservations` deliberately does not
  transition targets absent from an observation (an unreachable target must not
  flap-resolve). For facet baselines whose entities churn identity (certs), that
  leaves a stale open Finding for a departed entity. This is disclosed, not hidden;
  the fix (resolve facet-baseline Findings whose target left the View) is a
  deferred follow-up because it changes shared observation semantics.
- **No new Go dependency, no new EE image:** the Syncer's REST client is
  hand-rolled; the Actuator's python driver rides the existing `stratt-ee:dev`
  image (§1.4).
- **Contract count** rose to 15 embedded schema documents (cert.identity,
  cert.expiry, cert-issuer.input, intents/application, intents/certificate).

## Deferred / fast-follow (documented)
- Facet-baseline Finding resolution for entities that left the View (the stale-open
  gap above) — the highest-value follow-up.
- `Intent/FileSet` + `Intent/Access` kinds (the schema-at-the-seam machinery is now
  in place); `onRemove: revert` (lands with config/fileset).
- A real Action-execution framework so `revoke-cert` is a true §2.2 Action, not an
  Actuator. **Commitment (guardian flag):** cert-issuer is now the *second* capability
  (after webhook, ADR-0027) parked on the Actuator surface that charter-belongs on the
  Action surface — the Action/Actuator split is a §2.3 "deliberate and permanent"
  distinction, so this is disclosed drift that must not become permanent. The Action
  framework gets a Phase-3/4 commitment, not an open-ended defer.
- cert-manager / Vault / Venafi Connector variants (issuer-agnostic contract);
  the `exportable: true` admission policy (§7); per-cert remediation param binding
  so the renew/revoke Workflow targets a specific serial without an operator override.

## Runway after
Phase-3 board continues: Sites (NATS leaf) + pull agent/Bundles; CIS pack (pairs
with the Evidence store); audit→Splunk; HA/DR; SCIM.
