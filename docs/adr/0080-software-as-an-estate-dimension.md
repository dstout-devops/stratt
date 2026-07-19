# ADR 0080 — Software as an estate dimension: installed packages, open delivery-form, patch/advisory Findings

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES (MF-1 projection discipline promoted to enforced/test-proven INV-1/2; MF-2 §2.1 one-namespace-per-form single-owner; F-1 loud comparator as INV-3; F-3 meta-pattern reframed as heuristic); vocabulary-linter CLEAN
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (projections, never a second truth), §1.4 (boring spine; community owns breadth), §2.4 (Baseline/Finding/Evidence — one kind, framework-tagged), §9 (no ontology creep)
- **Grounded in:** the OS-package slice shipped with this ADR (`software.package` facet + the patch/advisory check)
- **Relates to:** ADR-0033 (compliance-as-data), ADR-0023 (`Intent/Application`, desired-side), ADR-0046 (`beam`/service coordinate), ADR-0079 (identity as a cross-cutting dimension)

## Context

Rounding out the estate (infrastructure ✓, identities ✓, policy ✓) means bringing **applications and services** into the graph. The naive model — an `Application` Entity that `exposes` a `Service` Entity that `runs-on` a `Host` — collapses on contact with reality:
- **Installation methods vary** (binary, script, OS package, Helm chart, Dockerfile, container). That is not what an application *is* — it is an **axis of variation**.
- **Software is developed internally or pulled in externally** — a lineage attribute, not a structural split.
- **A service may be cross-cutting across many applications, or entirely separate** — the app↔service relationship is a variable M:N, not a containment tree.

Forcing these into a fixed taxonomy is precisely the **writable-CMDB / universal-ontology** the charter permanently refuses. The identity work (ADR-0079) already found the charter-clean shape for exactly this problem, twice over, and this ADR applies it a third time.

## Decision

**Software is a cross-cutting projection DIMENSION, not a taxonomy — a Facet family + open form attributes + typed relations that entities opt into, grounded in a real connector, never an abstract ontology.**

### A recurring heuristic (derived from §1.1/§9, not new law)
The charter is the design authority; this is a heuristic *read of* it, not a new rule. Observed three times now — residency (`band`/`beam`, a computed coordinate), identity (a Facet family + relations, ADR-0079), and software — the charter-clean way to add a *domain* has been a **cross-cutting dimension (Facet family + open forms + typed edges), not a class hierarchy.** This **complements** Entity kinds, it does not replace them: `host`, `cert`, `user`, `group` remain real kinds; the dimension attaches *to* them. A useful gut-check when adding a domain — *am I adding a dimension/seam, or a rigid taxonomy?* — because a taxonomy of everything is the CMDB the charter refuses (§9, non-goals). It is guidance, not §-law.

### The deliverable-software dimension (shipped this slice)
- **`software.package` Facet** — the installed OS-package inventory on a host. `deliveryForm` (`package|container|chart|binary|script`) and `origin` (`distro|internal|external|vendor`) are **OPEN strings** (§1.4): a binary, a container, and an apt package are the *same dimension in different forms*, never separate Entity kinds — the same call identity made for `scheme`. Demanded by the patch/advisory check Contract (§1.1 sufficiency).
- **One namespace per form, one write-owner each (§2.1).** The software *dimension* is the `software.*` **family**, NOT a single namespace: `software.package` (OS packages), and later `software.container` / `software.chart` are **distinct Facet namespaces, each with a single registered write-owner** (its form's collector). Routing an apt collector, a registry reader, and a Helm reader all into one `software.package` namespace would be the §2.1 two-writers registration error — so we do not. `deliveryForm` stays a cross-form query field within a form's own facet; the write-owner is registered (à la `EnsureIdentitySubjectOwner`) when each form's collector ships (slice 2+). This slice ships the seam + the reader (the check); no production writer yet, so no owner is registered until the collector lands.
- **The patch/advisory check** — reads the inventory and raises a framework-tagged `patch/advisory` **Finding** for each installed package an advisory affects (the compliance-as-data lineage of ADR-0033). Remediation is a **patch Action against the host** (a package upgrade at the SoR), never a graph edit (§1.2). This is the value that made OS packages the right grounding: patching and vulnerability remediation.

### The software-component convention — one queryable surface across forms (slice 3)
Pressing the primitive convention: **every `software.<form>` facet is a list of COMPONENTS sharing one shape — `{name, version, origin, deliveryForm}`** — where the wrapper key names the form (`packages`/`containers`/`charts`) and the items are the common component. A container image's `name` is its repository and its `version` is its tag; a chart's are its chart name and version. This makes the software *dimension* **one queryable surface**:
- **`SoftwareAdvisory` targets a component by name**, not a package — a CVE is form-agnostic. The estate ruleset (`advisories/*.yaml`) declares `component:`, matched against packages, images, and charts alike.
- **`CheckSoftwareAdvisories` scans the whole `software.*` family in one pass** (`WHERE namespace LIKE 'software.%'`), extracting components form-agnostically. A CVE fires identically whether the vulnerable component is an apt package (`software.package`) or a base image (`software.container`) — proven cross-form in test. This is the convention's payoff: **one check over the dimension, N forms, N owners (one per namespace, §2.1).**

### Enforceable invariants (data-layer, not convention — matching ADR-0079)
Software inventory is the canonical writable-CMDB temptation, so the projection discipline is **enforced and test-proven**, never merely stated:
- **INV-1 — projection write-path (§1.2):** `software.package` attributes are writable **only** by Normalizers and Run provenance — the *same* data-layer ownership every Entity/Facet already passes through (the projector write-path + facet-ownership registry). An inventory authored directly would be a second truth. There is no code path that writes a `software.package` facet outside a registered collector.
- **INV-2 — the check is read-only; remediation goes to the SoR:** the patch/advisory check **reads** inventory and writes only `Finding`s — it **never** mutates a `software.package` facet or host state. Remediation is a contracted patch **Action** against the host (a package upgrade at the SoR), never a graph mutation. A test asserts the inventory facet is byte-identical after the check.
- **INV-3 — no silent false-negative (§1.8):** a version the comparator cannot rank with confidence (Debian/RPM epoch/tilde, non-numeric segments) is surfaced as an `unassessable` Finding for triage — **never** silently resolved to "not affected." On a security advisory, a hidden failure is worse than none.

### Applications and services are orthogonal dimensions, not a hierarchy
This ADR ships the **deliverable-software** dimension (what is installed/runnable). The **service/capability** dimension (what is provided — an endpoint, its consumers) is a **future sibling**, grounded in its own connector (a service registry / mesh / K8s Services) when one is built — never designed in the abstract here. They connect by **typed M:N relations** (`provides`, `depends-on`, `part-of`) whose *shape is whatever the projections observe*: "a service shared across many apps" and "separate" are two shapes of one relation graph, not two models. A service **has an identity** (ADR-0079 already reserved `scheme=service|workload`), so it inherits the identity seam rather than starting a new island.

### What this ADR does NOT do
- It does **not** mint an `application` or `service` Entity kind (no shipping Contract demands one yet; `Intent/Application` already owns the *desired* side — extend it, don't duplicate).
- It does **not** overturn ADR-0046: `beam` stays a computed residency coordinate. If a service dimension later lands, a host's `beam` becomes *derivable* from the services that run on it — the coordinate is the computed shadow of the projected Entity.

## Charter alignment

- **§1.1 / §9:** software is a Facet + open-form attributes at the connector seam, each Facet demanded by a shipping Contract (the patch/advisory check). No universal software ontology; no whole-Entity schema.
- **§1.2:** the inventory is a projection of the host's actual state; the advisory ruleset is declarable data; remediation flows to the SoR (a package upgrade), never a graph edit.
- **§1.4:** `deliveryForm`/`origin` are open — a community connector introducing a new form edits no core enum.
- **§2.4:** patch findings are the frozen `Finding` Kind, framework-tagged `patch/advisory` — no new noun.

## Consequences

- **Positive:** the estate now sees installed software and turns it into patch/vulnerability remediation signal; the dimension absorbs every delivery form without new kinds; a service dimension can join later via the identity seam and typed edges.
- **Negative / trade-offs:** the `software.package` facet holds an array (an inventory), coarser than attribute-facets — queried via JSONB; the version comparator is dotted-numeric only (full dpkg/rpm epoch/tilde semantics is a follow-up) but **fails loud, not silent** (INV-3: an unrankable version surfaces an `unassessable` Finding, never a hidden safe verdict); the advisory *source* and the *collector* that populates the facet are the next slice (this slice ships the seam + the read-only check, proven against seeded inventory).
- **Follow-ups (slices):** (2, **shipped**) the declarable advisory ruleset (`advisories/*.yaml`, compliance-as-data) + reconcile-cadence wiring of the check — the live patch loop, dormant until a collector projects inventory; (2b, **shipped**) a real OS-package collector — the salt Syncer, opt-in (`STRATT_SALT_COLLECT_PACKAGES`), runs `pkg.list_pkgs` and projects `software.package` per minion, claiming the single §2.1 write-owner only when collecting; best-effort (a pkg failure never blocks the grain sync, §1.8); fixture-tested against saltsim; (3, **shipped**) the `software.container` form + the form-agnostic check: `SoftwareAdvisory` over software components (name/version), `CheckSoftwareAdvisories` scanning the whole `software.*` family in one pass — a CVE fires across packages and images alike (`software.chart` slots in identically when a Helm reader lands, needing no check change); (4) the service/capability dimension + `provides`/`depends-on` typed edges (its own ADR, grounded in a registry/mesh connector); relation tombstone/GC before the dependency graph (ADR-0059 gap).

## Alternatives considered

- **An `Application` Entity with a fixed `exposes → Service → runs-on → Host` hierarchy** — rejected: it is the CMDB taxonomy the charter refuses, and it lies about the variable M:N reality (shared/cross-cutting services).
- **A kind per delivery method** (`HelmApp`/`PackageApp`/`ContainerApp`) — rejected: delivery form is an axis of variation, an open attribute — the same call identity made for cert-vs-user forms.
- **Software as its own Entity from day one** (a `package`/`software` node with `installed-on` edges) — deferred, not rejected: the first Contract (the patch check) is satisfied by a host facet; an Entity is warranted only when a fleet-wide/dependency Finding demands it (§1.1 sufficiency). Grounded, not speculative.
- **Overturn ADR-0046 and make `service` an Entity now** — rejected here: no connector demands it yet; the service dimension gets its own grounded ADR.
