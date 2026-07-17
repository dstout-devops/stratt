# ADR 0054 — Per-Step facet write-scope: narrow the write-back grant to what a Step declares

- **Status:** **Accepted** (2026-07-17, steward) — steward-directed (resolve the ADR-0051 F2 least-authority
  gap) + charter-guardian §2.5 design review (SOUND-WITH-CHANGES, four must-fixes folded into the Decision).
  Realizes §2.5 (least authority); does not edit the charter.
- **Vocabulary note:** this ADR deliberately uses **"write-scope"**, NOT "claim". "Claim types" is a frozen
  §2.4 Named-Kind concept — the *exclusive/additive ownership* an Assignment asserts on a Facet (the anti-GPO
  axiom). A per-Run write-back **authorization floor** is a different concept; overloading "claim" for both
  would conflate ownership with capability. ("Grant" is likewise taken — the plugin's registration ceiling.)
- **Date:** 2026-07-17
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.1 (facet-ownership — the single write owner of a namespace) · §2.5 (least authority)
  · §1.2 (projections; only Normalizers/Run-provenance write Entity attributes) · §1.8 (governance rejections
  surfaced, never silent) · resolves the ADR-0051 charter-guardian flag **F2** (the EE-Job grant is a fixed
  per-plugin facet list applied to *every* Step, not narrowed to what a given Baseline/Blueprint claims).

## Context

A plugin actuator's **grant** carries a fixed `FacetNamespaces` allowlist, set once at registration (e.g. the
ansible EE-Job grant: `os.kernel, os.hardening.{sysctl,sshd,filesystem,auditd,services}, fileset.content,
access.grants`). `pluginhost.govern` gates every write-back facet against it (`host.go:794`). This is the
**per-plugin ceiling** — the widest set the plugin may *ever* write.

But it is applied identically to **every** Step. A Baseline that only checks `os.hardening.sshd`, or a gather
Step that only projects `os.kernel`, runs under the *full* ceiling — so a compromised or buggy ansible Run
dispatched for a narrow Step can write back **any** of the plugin's granted namespaces, not just the one the
Step is about. The write-back blast radius is the plugin ceiling, not the Step's actual need — a least-authority
gap (§2.5) the ADR-0051 guardian flagged as F2.

The plugin **registration grant** is the right ceiling (a plugin can never exceed it). What's missing is the
**per-Step floor**: what *this* actuation is authorized to write. In scope: a per-Step facet **write-scope** and
its intersection with the grant at govern, for the **Actuator/Apply path only** (see the actuator-only scoping
below). Out of scope: identity-scheme/label scoping (facets are the blast-radius concern F2 named); a scope
language beyond a namespace list.

## Decision

Add a **per-Step facet write-scope** — the namespaces an actuation declares it will write — and make the
**effective write-back allowlist = the plugin's registered grant ∩ the Step's write-scope**, enforced at
`govern`. A write-back to a namespace outside the intersection is a governance **Rejection** (surfaced, §1.8),
even when the namespace is in the plugin's ceiling.

1. **The write-scope is an explicit authorization declaration, not content.** A `FacetWriteScope []string` field
   on the Step / Baseline / Trigger / Blueprint-derived Run (a first-class declaration field, like
   `credentialRefs`) — NOT inferred from the opaque `params` (that would re-introduce content-awareness, §1.4).
   It is declared in the platform's own **Facet** namespace vocabulary (§2.1 — platform API, not tool
   rendering), so enumerating namespaces is content-blind. It is the Step's single facet-output declaration
   (§2.3); there is no competing hand-list to diverge from it (guardian change #3).

2. **Intersection at the ONE governor (`govern`), pure set-intersection.** `RunInput.FacetWriteScope` threads
   through `executeJobPlugin`/`executePlugin` → `GovernStream`/`ApplyRaw` → the single `govern`
   (`host.go:722`) — the one choke both the gRPC Apply stream and the EE-Job stdout adapter feed; the
   intersection is NEVER re-implemented per transport. The facet gate (`host.go:794`) becomes: a facet `ns` is
   allowed iff `grant.allowsFacet(ns)` **AND** `inScope(ns)`. A namespace scoped-but-ungranted simply **DROPS**
   from the intersection — never a fallback, precedence, or widening (§2.4 anti-GPO: pure `AND`, no third
   branch). Two bounds (ceiling ∩ floor); the plugin can't exceed its grant, the Step can't write beyond its
   scope.

3. **Actuator-only (do NOT extend the floor to Syncers or Actions — guardian confirmation).** The write-scope
   narrows the **Actuator/Apply** write-back, the sole locus of F2's broad-ceiling blast radius. It is
   deliberately NOT applied to (a) **Syncer projection** (`toUpsert`, `host.go:982`) — a Syncer MUST project its
   full owned namespace each sync, so narrowing would break §1.2 projection completeness; nor (b) **Actions** —
   already tightly bounded by their per-Action output Contract (§2.3). A future reader must not "complete the
   symmetry" onto Syncers/Actions.

4. **Declaration-time lint: write-scope ⊆ the target Actuator's registered grant, failing at authoring
   (guardian must-fix #2, §1.8).** The govern-time drop closes the least-authority hole regardless — but under
   the TIGHT default a scope naming an ungranted namespace would silently intersect away and surface only as a
   confusing runtime facet Rejection (root cause invisible). So an **admission check** (where the actuator
   registry + grants are available — the API/reconcile boundary, §4.1 manifest-admission posture) rejects a
   declaration whose `FacetWriteScope` ⊄ the named actuator's registered facet grant, at authoring time. The
   govern-time intersection stays the non-bypassable security backstop; the admission lint is the §1.8
   diagnosis surface.

5. **Default posture — TIGHT: an absent write-scope means NO facet write-back.** An actuation writes back only
   what it scopes; least-authority by default (§2.5), and every Run's write-set legible from its declaration
   (§1.8). This is **not** over-reach (guardian confirmation): `grant.allowsFacet` is EXACT-MATCH over leaf
   namespaces, so even a general gather-facts play can always declare a valid maximal scope by naming its
   grant's leaf list — degrading gracefully to today's behavior for that Step while every narrow Step gets least
   authority. No legitimate facet-writing flow is wrongly blocked. (The rejected alternative — absent = the full
   ceiling — is the non-breaking half-fix that leaves F2 open for every undeclared Step; see Alternatives.)

**Pinned invariant:** the effective write-back allowlist is `grant ∩ write-scope`, computed at the ONE governor,
per call, as a pure `AND` (scoped-but-ungranted DROPS — no precedence/fallback, §2.4). The grant is the plugin
ceiling (registration); the write-scope is the Step floor (declaration). Neither is bypassable; every rejection
is surfaced (§1.8); a facet with no scope is never written — least authority, by structure.

## Charter alignment

- **§2.5 (least authority) — the core of the change.** A Step writes back only the namespaces it declares, not
  the plugin's whole ceiling; the write-back blast radius of a compromised/buggy run shrinks from "everything
  the plugin may ever write" to "what this Step is about." Directly closes F2.
- **§2.1 (facet-ownership) — reinforced.** The registered grant still establishes the single write *owner*; the
  write-scope narrows *which of the owner's namespaces this run touches*, never widens or reassigns ownership.
- **§1.2 (projections):** unchanged — only Run-provenance still writes, now within the tighter write-scope ∩
  grant bound; and the write-scope is Actuator-only, so Syncer projection completeness is untouched.
- **§1.8:** a scope-gated rejection is a surfaced governance Rejection (as the grant-gate rejections already
  are); the admission lint (Decision #4) surfaces a scope⊄grant mismatch at authoring; and a Run's write-back
  set is legible from its declaration — no hidden write authority.
- **§1.4 (content-blind):** the write-scope is an explicit typed declaration field in the Facet vocabulary,
  gated generically at govern — never inferred from tool content, so the core stays content-blind.
- **§2 vocabulary:** uses "write-scope", NOT "claim" (which is the frozen §2.4 ownership Named-Kind concept) —
  see the header note. `FacetWriteScope` names a per-Run authorization floor, distinct from an Assignment's
  ownership Claim and from the plugin registration Grant.

## Consequences

- **Positive:** least-authority write-back per Actuator Step (F2 closed); a Run's facet write-set is
  declaration-legible; the tight default makes new plugins/Steps safe-by-default; the mechanism is generic (any
  plugin actuator, not just ansible) and lives at the one governor.
- **Negative / trade-offs:** a real (bounded, pre-release) migration — every facet-writing Baseline/Step must
  gain a `FacetWriteScope` or its write-backs are rejected (there is **no pack-derivation yet** — see
  follow-ups, so this slice's baselines declare their scope explicitly); a new declaration field on
  Step/Baseline/Trigger + its schema; the scope threads through the RunInput → govern path (a new field on
  ApplyInvoke/GovernStream) and gains an admission lint.
- **Follow-ups:** **(a)** pack-derived scope — the CIS/OS-hardening pack does NOT yet expose a framework→facet
  namespace map, so auto-deriving a Baseline's scope from its framework (and **materializing** the derived scope
  on the compiled Baseline for Intent→Baseline→Run legibility, guardian change #4) is deferred until that map
  exists; until then baselines declare their scope. **(b)** run the `FacetWriteScope` identifier past
  `vocabulary-linter` for the exhaustive scan (the "claim" overload is resolved here; this confirms the rest).
  **(c)** if a prefix/wildcard grant syntax ever lands, the scope matcher must mirror `allowsFacet`'s semantics
  exactly (exact-match today makes this moot). **(d)** consider the same per-Step intersection for identity
  schemes/labels only if a blast-radius case arises there.

## Alternatives considered

- **Absent write-scope = the full plugin ceiling (scope only NARROWS, opt-in).** Non-breaking, but it leaves the
  F2 gap open for every Step that doesn't declare a scope — the default stays loose, exactly the half-fix the
  steward's directive rules out. Recorded as the migration-friendly fallback if the tight default's migration
  proves unacceptable, not the chosen posture.
- **Infer the scope from the Step's tool content (the play's projected facets).** Rejected: it re-introduces
  content-awareness into the core (§1.4) — the core would have to understand what an ansible play writes.
- **A free-standing hand-list divorced from the Step's output declaration.** Rejected (guardian change #3): the
  write-scope IS the Step's single facet-output declaration (§2.3); a second list that can diverge from what the
  Step declares it writes would be a second write-truth.
- **Per-Step grants issued at registration.** Rejected: grants are per-plugin (the channel identity, ADR-0047);
  a Run is not a registration. The write-scope is a per-*Run* narrowing of the standing grant, computed at
  govern — the right layer.

## Reviews

- **charter-guardian, §2.5 design review (2026-07-17): SOUND-WITH-CHANGES → folded above.** `grant ∩ scope` at
  the one governor is the textbook capability-narrowing shape (ceiling ∩ floor), confirmed to narrow-only (never
  widen/reassign §2.1 ownership), pure `AND` with no precedence (§2.4 anti-GPO), and the TIGHT default confirmed
  NOT over-reaching (exact-match leaf grants mean a general gather Step can always declare its full grant).
  Four folded changes: **MF-1** — rename "claim"→"write-scope" (the frozen §2.4 ownership overload); **MF-2** —
  add a declaration-time admission lint (scope ⊆ the actuator's registered grant, fail at authoring, §1.8), the
  govern-time drop staying the security backstop; **MF-3** — the write-scope IS the Step's single facet-output
  declaration, no divergent second hand-list; **MF-4** — materialize any pack-*derived* scope on the compiled
  Baseline (deferred with pack-derivation, which needs a framework→facet map that does not yet exist).
  Confirmed sound: Actuator-only scoping (NOT Syncer projection §1.2 / Actions §2.3), single governor (thread
  into `govern`, not per-transport), §1.2 projection unchanged.
