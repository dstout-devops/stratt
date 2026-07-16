# ADR 0050 — Certificate lifecycle as a reconcile Actuator (CSR/sign over the port)

- **Status:** **Accepted** (2026-07-16, steward) — reshapes certificate lifecycle from the imperative Action
  surface (Invoke issue/renew/revoke, ADR-0030) into a reconcile **Actuator** (Plan/Apply/Destroy over the
  sovereign plugin port), realizing ADR-0046's reconcile-is-primitive discipline for the first GA tenant. One
  charter-guardian pass (SOUND-WITH-CHANGES → folded; see Reviews).
- **Date:** 2026-07-16
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1 (type the seams), §1.2 (projections / two write paths), §1.6 (one identity /
  authz / audit), §1.8 (never hide failure), §2.1 (ownership), §2.3 (Actuator = reconcile), §2.5 (secrets
  brokered, never held). Builds on / supersedes ADR-0030 (§4, §5, §2); evolves ADR-0043 (its GC scope
  narrows); uses ADR-0046 (reconcile-is-primitive, content-blindness), ADR-0047 (§2 per-verb write path, §6
  ItemResult), ADR-0049 (Sites — edge cert issuance).

## Context

ADR-0030 GA'd `Intent/Certificate` as the first tenant of a reusable service class, driven by the
intent→Baseline→Finding→remediation loop: a facet-observation Baseline's `notBefore` threshold on `cert.expiry`
opened a Finding when a cert entered its renewal window, and a Workflow imperatively called the certissuer
**Action** (issue/renew/revoke). ADR-0030 §2 explicitly flagged this as *parked on the Action surface where it
charter-belongs, "must not become permanent."* ADR-0043 then had to GC per-serial Findings stranded when a
renewal minted a new serial (new Entity).

The steward's reframing: *"whatever state is just a reconciliation task, and the plugin is where issue-vs-renew
lives."* A certificate's desired state is "a valid cert for this commonName, not expiring inside the window";
whether convergence is an *issue* or a *renew* is domain mechanism — the plugin's semantic diff, not a
core-visible branch. That is precisely the Actuator/reconcile shape (ADR-0046), not three imperative calls.

## Decision

Certificate lifecycle is a **reconcile Actuator** on `plugins/certissuer` (Plan/Apply/Destroy), alongside its
existing Syncer. The Invoke/Action surface is retired for certs.

### 1. Reconcile unit = the Intent/commonName, not the serial-Entity

Desired state is `{commonName, role, ttl, renewBefore, csr}` (a plugin-boundary input Contract, §1.1). The
reconcile UNIT is the **commonName** (stable). The cert-serial Entity (`cert.serial` identity) is the OBSERVED
result — a serial churns on every renewal, so it can never be the desired-state key.

### 2. Plan is the plugin-owned semantic diff (content-blind core)

The plugin queries OpenBao for the current cert matching the commonName and decides: **no cert → issue**;
**cert exists and `notAfter` within `renewBefore` → renew**; **else → noop (empty)**. The core schedules an
opaque converge and never interprets cert state — this REMOVES the cert-specific `FacetExpectation.notBefore`
comparison from the core Baseline evaluator (ADR-0030 §4); that domain logic moves into the plugin where it
belongs.

### 3. Key delivery — CSR / sign, born-on-target (the highest-risk invariant)

**A cert this Actuator reports as converged MUST be a usable credential, delivered by a §2.5-clean path — never
a key silently discarded.** OpenBao `/issue` generates the keypair server-side and returns it once; discarding
it (the old Action's posture) ships a permanently-unusable cert and reports it green — a §1.8 failure the
abstraction must never hide.

So the op is **Sign, not Issue**: the target generates its own keypair + CSR **locally**; the Actuator submits
the CSR to OpenBao **`/sign/:role`**; only the signed cert (public) returns. **The private key is born on the
target and never crosses any wire** — strictly stronger than §2.5's "material never crosses the core." The CSR
(equivalently, the target's stable public key) is an input in desired state. **Renewal re-signs the same CSR/
key** → new cert, key unchanged: the cert churns, the key is stable (the correct renewal semantics). The
plugin never models `private_key` — there is none to model.

### 4. Model Y — reconcile-with-desired; a Gate binds intent, not a pinned plan

Cert convergence is **Apply-with-desired**: the plugin re-decides at Apply against live state. `PlanResponse.
plan` is **diagnostic only** — certs are deliberately **NOT** plan-as-artifact (ADR-0047 §8). A cert renewal is
a small, deterministic converge, not a large reviewable blast-radius plan to freeze; forcing the Terraform
saved-plan pin onto it buys nothing and contradicts re-decide-at-Apply. A **Gate** (when present) binds the
**desired state + trigger condition** ("renew CN=X iff within `renewBefore`, current serial S"), not a frozen
imperative op; `ItemResult` honestly reports the re-decided outcome (issued-new-serial / renewed / converged-
noop). Routine in-window renewal is **ungated** pure reconcile; **revoke** and (by policy) first-issue-into-
prod are gated. Two Gate models thus coexist deliberately: tofu pins a digest (§8), certs bind intent+trigger.

### 5. Idempotency — the reconcile loop converges; `/sign` is not invariant-#7 idempotent

`/sign` (like `/issue`) is non-idempotent and non-transactional; a retry after a lost ack can double-sign.
This is **not** claimed as invariant-#7 idempotency. The honest guarantee is **eventual convergence to exactly
one valid cert per `(commonName, role)`**: the next Observe sees a duplicate and reconcile **revokes the
extra**, accepting a brief extra-cert window. A Temporal `idempotency_key` suppresses the in-process retry dup;
it does not cover cross-partition lost-ack — the loop does. `ItemResult` never over-claims a guarantee (§1.8).

### 6. Destroy — the gated destructive exception

Revoke is the **Destroy** verb, gated (§1.6 drawn exception): a Gate under the one authz/audit model, reachable
by any authorized Principal (human OR agent, ADR-0047 §8 approve-what-you-see) — not human-exclusive. Destroy
tombstones the cert Entity (symmetry with the Syncer's liveness).

### 7. Write-back stays host-validated; compliance stays Baseline/Finding

Content-blindness is the **Apply-decision** rule, not the write-back rule. The cert Entity + `cert.identity`/
`cert.expiry` facets written back at Apply are **core-schema-validated** (host `WriterRun` + `ValidateFacet`,
ADR-0047 §2) — the plugin PROPOSES `ObservedEntity`; the host stamps provenance and validates. **Two drift
paradigms, stated explicitly:** *renewal* is reconcile (the Plan diff drives converge — no Finding); *cert
compliance propositions about a serial-Entity* (issuer, keyUsage, `exportable:false`, CIS/PCI) stay
**Baseline/Finding** with auto-sealed Evidence (ADR-0030 §6, ADR-0029) — that audit/export surface is
PRESERVED, not dropped. The Run history + provenance is the audit trail for the reconcile (renewal) path.

## Consequences

- **Positive:** cert lifecycle becomes reconcile-native (ADR-0046); issue-vs-renew is the plugin's Plan, core
  stays content-blind; the CSR/sign path is strictly §2.5-stronger (key never leaves the target); "does CN X
  have a valid cert" now falls out of reconcile (the coverage gap ADR-0043 deferred); the cert-specific
  expiry-threshold leaves the core Baseline evaluator; edge cert issuance rides ADR-0049 (Sites over the port).
- **ADR-0043 narrows, does not vanish:** the per-serial *expiry* Finding no longer has a producer (dissolved),
  but the estate-wide GC sweep stays (it also GCs check-Run compliance), and any cert-Entity *compliance*
  Baseline still anchors a Finding to the churning serial → the sweep stays load-bearing for those.
- **Supersedes:** ADR-0030 §4 (notBefore-baseline drives renewal → the plugin's Plan) and §5 (gated-revoke
  Workflow → the Destroy verb); **reverses ADR-0030 §2** (cert is now *deliberately* an Actuator, resolving the
  "parked on Actions" tension in a principled way) and **retires that dangling Action-framework commitment for
  certs** so it cannot silently rot.
- **Negative / cost:** the target must generate its own keypair + CSR (the born-on-target requirement — a real
  integration contract, not free); a brief extra-cert window exists under lost-ack before the loop revokes the
  duplicate; two Gate models (pin-digest vs intent+trigger) coexist and must be documented per actuator.

## Alternatives considered

- **issue-to-broker + provisioned CredentialRef** (Apply `/issue`, store {cert,key} in the plugin broker,
  return the CredentialRef name). §2.5-clean and fits provision-the-key cases, but needs an additive
  `ApplyResponse.provisioned_creds[]` (it exists only on `InvokeResult` today) and keeps a key in a broker.
  Rejected as the default in favor of born-on-target (no key exists to leak); retained as the future option for
  ephemeral service certs Stratt provisions.
- **Model X — pinned cert plan, fail-closed** (reuse ADR-0047 §8). Consistent one-Gate-mechanism, but freezing
  a small deterministic cert converge adds friction without the blast-radius payoff that justifies the tofu
  pin; rejected for cert domain-fit.
- **Keep the imperative Action** (ADR-0030 status quo). Rejected: it is the "parked, must-not-be-permanent"
  surface, and it cannot express reconcile-to-desired without re-implementing the loop in a Workflow.

## Reviews

- **charter-guardian, pass 1 — cert reconcile-Actuator (2026-07-16): SOUND-WITH-CHANGES → folded.** Two
  blocking flaws: (1) §2.5/§1.8 — "issue then discard the key" ships a dead cert reported green → resolved to
  CSR/sign born-on-target (§3), the highest-risk invariant. (2) §1.8/ADR-0047 §8 — a pinned cert plan and
  idempotency-by-CN contradict → resolved to Model Y reconcile-with-desired, plan diagnostic-only (§4). Folded
  must-fixes: idempotency honesty + exactly-one-per-(CN,role) + revoke-the-extra (§5); host-validated
  write-back (§7); Destroy gated but not human-only (§6); compliance/Evidence preserved + two-drift-paradigm
  relationship stated (§7); ADR-0030 §4/§5 superseded + §2 Action-commitment retired.
