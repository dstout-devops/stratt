# ADR 0081 — Service as a capability dimension: the deliverable↔service seam, grounded in K8s + Helm

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES (ADR-0046 revisit approved as *composition, not overturn*; MF-1 INV-1 data-layer teeth incl. the edge, MF-2 corrected relation-safety grounding, MF-3 `release`→form-neutral `application`, MF-4 beam-guardrail INV — all folded); vocabulary-linter CLEAN (final code identifiers re-linted at the slice-1 merge per CLAUDE.md)
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (projections, never a second truth), §1.4 (open forms; community owns breadth), §2.4 (Finding), §9 (no ontology creep)
- **Revisits:** ADR-0046 (`beam`/service as a computed coordinate) — composition, with the guardian's sign-off
- **Relates to:** ADR-0080 (software dimension — the orthogonal deliverable dimension), ADR-0079 (identity dimension — a service *has* an identity), ADR-0059 (relations; `relation_write_path`; the relation-GC gap this sequences around)

## Context

Rounding out the estate (infrastructure ✓, identities ✓, policy ✓, software ✓) needs the **service/capability** dimension. It has real rough edges, and Helm is where they concentrate — so this ADR names them rather than papering over them.

**The first convention test — and the trap.** The tempting move is `software.service`, a fourth software form. **Wrong.** Software is a *deliverable* (what is installed/run); a service is a *capability* (what is provided). A service's essence is endpoint + protocol + **consumers/dependencies**, not `{name, version}`. They are the **orthogonal dimensions ADR-0080 already named**, connected by a variable M:N. Over-unifying them is the first way this fails.

## Decision

**Service is a distinct CAPABILITY dimension — a `service` Entity carrying `service.*` facets, connected to the deliverables that provide it by a typed M:N relation — grounded in a real SoR (K8s Services + Helm), never designed in the abstract.**

### 1. `service` is an Entity, not a facet (composing with ADR-0046)
A package was a facet on its host (no independent identity or edges). A service is different: an **endpoint**, **dependencies/consumers** (edges to *other* services), and an **identity** (it is `scheme=service`, carries `identity.credential` for its mTLS/SPIFFE cert — the identity seam ADR-0079 reserved). A thing with its own identity and edges is an **Entity**.

This **composes with ADR-0046** (whose guardian ruling constrained `beam`/`band` to *optional, sparse, platform-computed coordinates, never hand-authored, never total* — it ruled on beam-as-a-*coordinate*, not "service may never be an Entity"): **`beam` stays the computed residency *shadow*** (a host's `beam` derives from *which services run on it*), and the `service` Entity is the projected capability that gives `beam` its derivation source. ADR-0080 (accepted, guardian-reviewed) forecast exactly this; 0081 executes the forecast.

- **INV-BEAM (MF-4):** the revisit **tightens** 0046, it does not loosen it — `beam` remains **optional, sparse, platform-computed, and never mandatory or hand-authored**, even now that its derivation source (the `service`→host residency) exists. A derivation source is not a licence to make `beam` total.
- **Vocabulary (per vocabulary-linter):** the `service` **Entity** (a projected capability/endpoint) is distinct from the `service` **Principal kind** (`human|service|agent`, an authz identity role) and from the `beam` coordinate — different planes, no collision, exactly as `user`/`group` Entities coexist with the `human` Principal kind (ADR-0079).

### 2. `service.endpoint` Facet (the capability seam)
Ports, `protocol` (**OPEN**: `http|grpc|tcp|udp|queue|…` — an axis of variation stays a field, not a namespace-per-protocol, §1.4), cluster address, and the selector. Demanded by the K8s Services collector's Contract (§1.1 sufficiency). **No `service.endpoint` JSON Schema hardens ahead of that collector Contract** — slice 1 co-ships both.

### 3. The deliverable↔service M:N — the Helm seam, made honest
A Helm release is **not** a service; it is a **deliverable** projected as an **`application` Entity** (a form-neutral kind — the *observed* deployed deliverable instance; see naming below) carrying a `software.chart` facet (a *third* software form, which slots into ADR-0080's form-agnostic advisory check with **zero** change: a vulnerable chart version surfaces a `patch/advisory` Finding for free). It **provides** the services it renders:
- **One chart → many services** (web + worker + cache = 3 services, 1 application), occasionally one service ← many applications — the canonical **M:N** an "app *contains* services" hierarchy lies about.
- A **`provides`** Relation (`application` → `service`) carries it. The edge is **DERIVED, not guessed**: K8s stamps `app.kubernetes.io/managed-by=Helm` + `app.kubernetes.io/instance=<release>` on a release's objects, so "this application provides that service" is a projected correlation from real labels.
- One K8s scrape feeds **both dimensions and the seam between them**: the `service` Entity (`service.endpoint`), the `application` Entity (`software.chart`), and the `provides` edge.

**Naming (MF-3, form-neutral per §1.1/§9 and the marketable-names rule):** the deliverable-instance Entity is **`application`**, not `release`. `release` is Helm-flavored and re-litigates ADR-0080's rejection of kind-per-form (`HelmApp`/`PackageApp`) at the Entity level; `deployment` collides with the K8s Deployment resource. `application` names what the thing IS — a deployed deliverable instance — form-neutral: a Compose stack or Kustomize app is an `application` too, carrying whatever `software.<form>` its `deliveryForm` names. The **observed `application` Entity** (projected from Helm/K8s) is distinct from **`Intent/Application`** (the *desired* declaration, ADR-0023) — the healthy observed/desired split the platform has everywhere, not a duplicate.

### 4. Enforceable invariants (data-layer, to ADR-0079/0080's bar — MF-1)
A "service catalog" is the textbook writable-CMDB shape, so the projection discipline is **structural and test-proven**, not stated:
- **INV-1 — projection write-path (§1.2), facets AND the edge:** `service.endpoint`/`software.chart` are writable only by Normalizers/Run provenance via the **`enforce_write_path`** facet trigger; the **`provides` edge** is writable only on the same paths via the **`relation_write_path`** trigger (ADR-0059 — because `provides` is a *Relation*, facet enforcement alone does not cover it). Slice 1 ships a test asserting no code path writes `service.*`/`software.chart` or a `provides` edge outside the registered collector. A service is a rebuildable projection of its SoR — never a writable service catalog (the writable-CMDB non-goal).
- **INV-2 — remediation to the SoR:** a service/deliverable change (scale, redeploy, dependency fix) is an Action against K8s/Helm/Git, never a graph edit.
- **§2.1 ownership:** `service.endpoint`, `software.chart`, and the `provides` edge each have a single registered write-owner (the K8s collector).

## Rough edges, acknowledged (not hidden — §1.8)
- **Why `provides` is safe now but `depends-on` is not (MF-2, corrected).** `provides` is safe **because it is single-owner and single-source**: the K8s collector *fully enumerates* Helm-labeled objects each sync and does a **per-source delete-and-replace of the `provides` edges it owns** — a bounded full-sync boundary within one source, so a removed service's edge is replaced away, never dangling. (This is NOT the global relation tombstone/GC that ADR-0059 lists as an *open gap* — my earlier draft mis-cited ADR-0042, which is *entity*-liveness, not relation-GC.) `depends-on` (service→service) is **multi-source** (a mesh AND declared deps) with no single full-enumeration boundary, so it genuinely needs cross-source relation-GC and is therefore a **later slice**. Slice 1 MUST implement the per-source edge replacement; if it cannot, `provides` waits alongside `depends-on`.
- **SoR plurality:** services come from K8s, meshes, gateways, OpenAPI. This ADR grounds in **K8s Services + Helm labels** (the fundamental, universally-present SoR); others are later sources into the same `service` kind, single-owner-gated (§2.1).

## Charter alignment
- **§1.1/§9:** service is a dimension (Entity + Facet family + typed edges), demanded by the K8s collector Contract — not a universal service/topology ontology. `provides`/`depends-on` are *typed* edges (free-string relation kinds, zero core cost, ADR-0059/0055), not an OR-language; the `depends-on` deferral is the anti-DSL discipline working.
- **§1.2:** everything projects from K8s/Helm; remediation flows to the SoR; the graph correlates, never authors (INV-1, structural).
- **§1.4:** `protocol` is open. **§2.4:** chart CVEs reuse the frozen `Finding`, framework `patch/advisory`.
- **ADR-0046:** reinforced (INV-BEAM), not overturned.

## Consequences
- **Positive:** the estate sees services and their providers; the deliverable↔service M:N is honest (no containment lie); `software.chart` gives charts vuln-findings for free; a service is `scheme=service`, so mTLS/SPIFFE `identifies` it (a later slice). Infra→software→service→identity now traverse.
- **Negative / trade-offs:** revisits ADR-0046 (guardian-approved as composition); introduces `service` + `application` Entity kinds, `service.endpoint`/`software.chart` facets, a `provides` edge — real new surface; `depends-on` (the richest payoff) waits on relation-GC.

## Slice roadmap (each ADR/Contract-gated)
1. **K8s + Helm collector** (this dimension's slice 1): project `service` Entities (`service.endpoint`) from K8s Services and `application` Entities (`software.chart`) from Helm-labeled objects, plus the `provides` M:N edge — the collector doing a **per-source full-sync delete-and-replace** of its `provides` edges. Fixture-tested against a fake clientset. Tests: INV-1 (no write outside the collector, facets + edge); the "chart CVEs for free" claim (that `software.chart` conforms to ADR-0080 slice-3's `{name, version, origin, deliveryForm}` component shape so `CheckSoftwareAdvisories`'s `software.%` pass matches it). Single §2.1 owner. Final identifiers re-linted via vocabulary-linter at merge.
2. **`depends-on`** — *after* relation tombstone/GC (ADR-0059 gap): a mesh/declared dependency source.
3. **service ↔ identity** — `identifies` from a service's mTLS/SPIFFE cert to the `service` Entity (ADR-0079 seam).
4. **other SoRs/forms** — gateways, OpenAPI, non-Helm deployables (each an `application` via its own `software.<form>`).

## Alternatives considered
- **`software.service` (a fourth software form)** — rejected: capability ≠ deliverable; the component `{name,version}` shape does not fit a service; orthogonal dimensions.
- **`app` Entity that *contains* `service` Entities (a hierarchy)** — rejected: it lies about the M:N. The `provides` relation models the real shape.
- **`release` Entity kind** — rejected (MF-3): Helm-flavored, re-litigates 0080's kind-per-form rejection at the Entity level. `application` is form-neutral. `deployment` also rejected (collides with the K8s Deployment resource).
- **Keep service as `beam` only (ADR-0046 unchanged)** — rejected: a coordinate carries no endpoint, edges, or identity; `beam` is retained as the computed shadow.
- **Ship `depends-on` now** — rejected: no cross-source relation-GC yet (ADR-0059); multi-source dependency edges would dangle.
