# ADR 0136 — Superseded, not integrated: which external systems are terminal and which are forever

- **Status:** **Proposed** (2026-07-26, steward). Charter review by hand; §1 non-goals and §6 answered
  inline. **No new dependency. No code change.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** § Positioning ("the successor to AWX/AAP"), §6 (AWX exodus), §1 (permanent
  non-goals — MDM protocol implementation), §1.4 (boring spine, pluggable everything), §1.5 (sovereign
  contracts)
- **Reconciles with:** ADR-0026 (the `/api/v2` façade), ADR-0086 + ADR-0127–0133 (the `ansible.*`
  mirror arc), ADR-0039 (salt), ADR-0135 D4 (**the case that produced this ADR**)

## Context

ADR-0135 drafted AWX as a capability provider, built a migration story on binding to it, and had to
withdraw both. The mistake was not careless — it was **structural**, and it will recur.

Nine ADRs of `ansible.*` mirroring make AWX the most deeply modelled external system in the repo.
Chef, Puppet and Salt have plugins too. Read the ADR corpus alone and that depth reads like a
maturing partnership. **The replacement intent exists only in the charter**, and nothing in
`docs/adr/` says a word about a sunset — so anyone working from the decision record, as a contributor
naturally would, will reach the same wrong conclusion.

The fix is not more prose about vision. It is a **rule with consequences**, in the corpus where the
drift happens.

## Decision

### D1 — Two classes of external system, split by one question

_Does Stratt intend to still be talking to this in five years?_

**SUPERSEDED (terminal).** Management **control planes** for tools Stratt already runs directly:
AWX/AAP, Chef Infra, Puppet, Salt, SCCM. Stratt executes `ansible-playbook`; what AWX adds is a GUI,
an RBAC model, a scheduler and a job store — all of which are Stratt's spine (Temporal, OpenFGA, the
graph). There is no durable role left. **The estate's goal is to switch them off.**

**DRIVEN (permanent).** Systems of record and infrastructure APIs whose function is not Stratt's to
absorb: vCenter, AWS, NetBox, OpenBao, Kubernetes, **Intune and Jamf**. Reimplementing an MDM protocol
is a **permanent non-goal** (§1) — Stratt drives these forever, and a deeper integration with them is
a feature, not drift.

The split is by **intent**, not by current surface area. `msgraph` is a Syncer today and permanent;
`ansible-automation` is a Syncer today and terminal. Surface area says nothing about which list a
system is on.

### D2 — A superseded platform gets exactly two surfaces, and they both point outward

1. **Read in** — a Syncer projecting its estate so it can be seen, audited, and reasoned about.
2. **Import out** — a materializer converting its objects into Stratt CaC. This is the exodus.

**Never** a capability provider, an Actuator, a remediation target, or a write-back path. Each of
those creates a dependency the estate cannot then switch off, which is the definition of failing to
supersede. ADR-0135 D4 applied this to AWX before the rule had a name; this is the name.

The `/api/v2` façade (ADR-0026) is not a third surface — it is Stratt **wearing AWX's interface** so
existing tooling survives cutover. Traffic terminates at Stratt. That distinction is the whole
decision in miniature: **Stratt executes; the superseded platform does not.**

### D3 — "Done" is a stated exit condition, per platform

A supersession with no finish line is an integration with better marketing. Each terminal platform
carries its exit condition in its own ADR, and AWX's is already implied by §6 — every job template
imported as a Step preset, every inventory a View, façade traffic served by Stratt, **AWX switched
off**. The import target is frozen at 24.6.1 forever, so the target does not move.

### D4 — The point of superseding is the estate, not the scalp

Replacing AWX is worth nothing on its own; a better job runner is explicitly _not_ the thesis
(§ Positioning). It is worth doing because **a control plane that owns a slice of the estate prevents
anyone from managing the whole of it**. Every point tool keeps its own inventory, its own RBAC, its
own idea of what a host is — and the seams between them are where the industry's duct tape lives.

So supersession and seams are one idea, not two: Stratt takes the control planes out of the way, and
puts back a **typed graph** (one identity per Entity), **capability classes** (a dependency on a
contract, never a vendor — ADR-0104), and **one standard path** to deploy, configure and reconcile —
Intent → Blueprint → Baseline → Finding → remediation, identical whether the target is a VM, a
container, a certificate or a file. Driven systems plug into that path; superseded ones are removed
from it.

## Consequences

- **A contributor reading only `docs/adr/` now learns the intent.** That was the actual gap.
- **Proposals gain a test**: does this make a terminal platform harder to switch off? A launch Action,
  a bidirectional sync, a capability binding — each fails it. This ADR is meant to be cited in review.
- **Deeper integration with a DRIVEN system is not drift**, and saying so protects the vCenter/NetBox/
  OpenBao work from a rule aimed at a different problem.
- **The lists will need revising.** A platform can move — if Stratt ever drove a tool it does not run
  directly, that is a new argument, made in a new ADR, not a quiet reclassification.
- **This decides no schedule.** Nothing here says when AWX support ends, and an exit condition is not
  a deprecation notice.

## Alternatives considered

- **Leave it in the charter.** The status quo, and it demonstrably failed: ADR-0135 was drafted
  against the charter by someone who had read it. Decisions get made in the ADR corpus.
- **State the vision without D2's rule.** Prose nobody can cite in a review. The rule is the artifact;
  the rest is context for it.
- **Name every platform now, exhaustively.** Rejected — the two lists are examples of a test, not a
  registry. A closed list would be wrong within a quarter and would invite arguing membership instead
  of applying the question.
- **Fold this into ADR-0135.** Rejected: 0135 is about what a plugin ships. This is about which
  external systems are terminal, and it will be cited by ADRs that have nothing to do with plugin
  packaging.
