# ADR 0127 — One `ansible-automation` plugin, two Sources

- **Status:** **Proposed** (2026-07-25, steward). Charter review by hand (this session's rules bar the
  subagent); §1.2/§1.4/§2/§2.1/§9 answered inline. **No new dependency.** Vocabulary reviewed by hand
  against §2's banned-identifier list — see D2.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth), §1.4 (boring spine, pluggable
  everything), §1.8 (never hide diagnosis), §2 (frozen vocabulary; namespace collisions are a real cost),
  §2.1 (Source ownership), §9 (no ontology creep)
- **Reconciles with:** ADR-0042 (cross-source liveness), ADR-0048 (integration taxonomy), ADR-0085
  (relation-presence Baseline / the `runs` edge), ADR-0089 (the AWX→CaC transform is plugin breadth),
  ADR-0103 (runtime connector registry)

## Context

Red Hat's Ansible Automation Platform presents itself to an operator as **one system**. Stratt currently
meets it with **two plugins that never mention each other**:

| Plugin           | LoC  | Projects (`FacetNamespaces`)                                                                |
| ---------------- | ---- | ------------------------------------------------------------------------------------------- |
| `awx`            | 3417 | `ansible.template`, `.workflow`, `.schedule`, `.org`, `.team` (+ adopt/materialize Actions) |
| `ansibleproject` | 675  | `ansible.playbook`, `.role`, `.collection`, `.inventory`                                    |

Nothing about that split is legible from outside. An operator connecting "their AAP" installs two things
with two names, one of which (`ansibleproject`) names an AWX noun rather than the product. So the surface
is renamed and unified: **one plugin, `ansible-automation`.**

### The research that set the targets

An AWX/AAP **Project** is one object unifying four things (`/api/v2/projects/{id}/`, `/playbooks/`,
`/inventories/`, `/project_updates/`): an SCM pointer (`scm_type` git|svn|insights|archive|manual,
`scm_url`, `scm_branch`, `scm_refspec`, credential); a sync lifecycle (`scm_revision`,
`update_on_launch`, `cache_timeout`, clean/delete/track-submodules, `allow_override`); a content catalog;
and supply-chain verification (`signature_validation_credential` — GPG; tampered content does not run).
Its sync installs from **both** `roles/requirements.yml` and `collections/requirements.yml`.

Against that bar, our two halves are not merely separate — **they share no project identity**. The
`ansible` shim clones repo X at ref R at run time (ADR-0025); the content Syncer catalogs directory Y
under a free-string `ProjectID`, and projects `ansible.playbook` identities like `webproj/site.yml` with
**no revision in them**. Nothing binds catalogued content to executed content, which matters because
**ADR-0085 already ships a governance signal that rests on it**: a job template whose playbook is not
projected has no `runs` edge and raises an orphan Finding. ADR-0085 anticipated projection _lag_; it did
not anticipate the Syncer cataloguing a different tree than the Run executed.

### The trap this ADR exists to avoid

The obvious reading of "consolidate" — one plugin, one identity, one Source — **silently destroys data**.

`Grant` carries exactly one `Source`; every Observe is a **full sync** driving a per-Source sweep; and
ADR-0042 made liveness a **union over Sources** via `graph.entity_presence(entity_id, source_id)`. So if
the Controller half and the content half shared a Source, an Observe that read only the Controller would
drop the presence rows of every content artifact — and vice versa. The mirror would blank half of itself
on alternate ticks.

Two further properties depend on the split, and both are deliberate:

- **ADR-0085's `runs` edge is CROSS-source on purpose.** AWX holds `ansible.playbook` as a
  **pointable-only IdentityScheme, never a FacetNamespace**, so an edge to a playbook no content root
  projects is _dropped, never vivified_ (ADR-0042/0082). One owner of both ends collapses that, and the
  orphan signal stops meaning anything.
- **`Source.Name` is per-Controller** (`awx-<ctrlID>`) because §2.1 requires two Controllers never share a
  tombstone key. Content roots carry their own. One Source cannot serve both.

## Decision

### D1 — One plugin, **two Grants, two Sources**

`ansible-automation` is one module, one binary, one manifest, one name in the estate — and registers
**two Grants**, hence two `graph.source` rows:

| Half         | `Source.Kind`        | `Source.Name`     | Owns                                                          |
| ------------ | -------------------- | ----------------- | ------------------------------------------------------------- |
| `controller` | `ansible.controller` | per Controller id | `ansible.template`, `.workflow`, `.schedule`, `.org`, `.team` |
| `content`    | `ansible.content`    | per content root  | `ansible.playbook`, `.role`, `.collection`, `.inventory`      |

**The unification is of the surface, not of the ownership.** That is the whole decision: an operator sees
one plugin; the graph keeps two tombstone keys, two presence lineages, and the cross-source edge ADR-0085
rests on. Structurally this is already supported — two hosts may dial one plugin address under different
grants — so no port change is needed.

**Why not one Source with a smarter sweep** (e.g. sweeping only the namespaces the Observe covered):
because it would make the sweep's correctness depend on a plugin correctly reporting _what it looked at_,
turning a data-layer invariant into a plugin promise. §1.2 puts these guarantees in the data layer
precisely so they do not rest on plugin good behaviour.

### D2 — The name is `ansible-automation`

Chosen over three alternatives, each rejected for a stated reason:

- **`ansible-tower`** — a brand Red Hat retired years ago. Naming a new seam after a dead product is a
  §1.7-adjacent liability: it is wrong on day one and gets wronger.
- **`ansible-awx`** — precise about the API (AAP's controller _is_ AWX, and `/api/v2` is the AWX API) but
  narrower than the system, and redundant: AWX is already Ansible's.
- **`ansible-platform`** — maps to AAP, but **`platform` is heavily overloaded here** (the control plane,
  the Platform Gateway parity component, the platform MCP server). §2 treats namespace collisions as a
  real cost — it is why `Sink` is not called `Channel` — and this is the same call.

`ansible-automation` names the product family without claiming an overloaded word, and survives the next
rebrand of AAP.

**Vocabulary check (§2), by hand.** The name introduces no banned core-model identifier. `inventory` and
`playbook` remain **tool-namespaced Facet namespaces** (`ansible.inventory`, `ansible.playbook`), which is
what §2 licenses: the ban is on core-model identifiers, and these are exactly the "tool-specific
rendering" the vocabulary section describes. No Named Kind is added. `Source.Kind` becomes
`ansible.controller` / `ansible.content` — namespaced, and no longer the bare `awx` / `ansible-project`.

### D3 — Migration rides ADR-0042's union liveness, and is therefore boring

Changing `PluginIdentity` and `Source.Kind` stamps a **new** `graph.source`, and this is the risky-looking
part of the change. ADR-0042 already makes it safe: **an Entity is live while ≥1 Source observes it.** So
the sequence is additive, with no window in which anything is retracted:

1. The new Sources register and observe. Every entity now has presence rows under **both** the old and new
   Source — live under the union, unchanged for every consumer.
2. Once the new Sources have completed a full sync, the old Sources are deregistered and removed. Presence
   rows drop with them; the entities stay live on the new rows.

**No entity is ever momentarily unobserved**, which is the property that would otherwise fire spurious
orphan Findings and drift. Provenance on already-written Facets keeps naming the old Source — correct, and
deliberately not rewritten: provenance records **who wrote it at the time**, and back-dating it would be a
second truth (§1.2).

### D4 — What this ADR does NOT do

Stated because a rename is exactly the change that quietly grows scope:

- It does **not** merge the ownership split, the identity schemes, or the `runs` edge's cross-source
  character (D1).
- It does **not** add `ansible.project`, `scm_revision`, containment Relations, or
  `roles/requirements.yml` parsing. Those are the researched targets and each is a real decision — the
  revision one in particular repairs ADR-0085's soundness and deserves its own argument, not a paragraph
  inside a rename.
- It does **not** decide content signature validation. AAP validates GPG signatures at project sync; our
  answer is byte-pinning (ADR-0117 i / ADR-0124), which is **stronger for reproducibility and silent about
  authorship**. Since §7.3 already commits to cosign/sigstore, adopting GPG _because AAP does_ would be
  the wrong reflex. It belongs with SUP-1, not here.

## Charter alignment

- **§1.2.** Two Sources keep the mirror's per-source presence and tombstone semantics intact; the sweep
  stays a data-layer invariant rather than a plugin promise.
- **§1.4.** One plugin per external system is the pluggable-everything shape; nothing moves into core.
- **§2 / §2.1.** No new Named Kind; no banned core-model identifier; Source ownership stays per-Source and
  per-Controller as §2.1 requires.
- **§9.** No new namespace is invented — the four `ansible.*` content namespaces and five controller ones
  are exactly today's.

## Consequences

- **Positive.** An operator installs one thing named after the system they actually run. The two halves
  become reviewable together, which is what makes the researched targets (a shared project identity, the
  containment edges) expressible at all. `ansibleproject` stops naming an AWX noun.
- **Negative / trade-offs.** It is a wide mechanical change — ~4,100 lines across two modules, two boot
  blocks, deploy manifests, estate declarations, the ADR-0103 migration tracker, and the ADRs that name
  the plugins. The migration needs both old and new Sources briefly registered together, so an operator
  upgrading sees two Sources for one system until step 2 completes; that is visible in the UI and is
  called out rather than surprising. Provenance on pre-migration facets keeps naming the old Source
  forever — correct, but it means `graph.source` history is not uniform.
- **Follow-ups booked (not absorbed):** `ansible.project` + `scm_revision` binding catalogue to execution
  (and with it, ADR-0085's soundness); containment Relations; `roles/requirements.yml`; content signature
  validation under SUP-1.

## Alternatives considered

- **One plugin, one Source.** Rejected in D1: it silently retracts half the mirror on every full sync, and
  collapses the cross-source `runs` edge ADR-0085 depends on.
- **Leave both plugins as they are.** Defensible — `vcenter`/`netbox`/`openbao` all name their product, so
  `awx` was never wrong — but it leaves an operator installing two differently-named things for one
  system, one of them named after an internal AWX noun. The surface is the reason to change; the ownership
  is the reason not to change it further.
- **Merge only the repos, keep two plugin identities.** Rejected: two identities means two manifests and
  two registrations under different names, so the operator-facing problem is untouched — all of the churn,
  none of the benefit.
