# ADR 0169 — The last door before something runs

- **Status:** **Proposed** (2026-08-04, steward). Charter review by hand — this session's rules bar
  the subagent; §1.4/§1.8/§7.3 answered inline. **No new dependency.**
- **Date:** 2026-08-04
- **Deciders:** steward
- **Charter sections:** §7.3 (pinned-digest images), §1.4 (boring spine — few dependencies),
  §1.8 (never hide diagnosis)
- **Completes [ADR-0168](0168-a-warning-is-not-a-gate.md)** at the door it cannot see, and takes a
  bite out of [ADR-0165](0165-there-has-never-been-a-release-to-sign.md) D6's booked in-cluster half.

## Context

ADR-0168 made an unpinned image a render failure. It gates the images the **chart** deploys: the
control plane, the agent, the forwarder, plugin pods.

**It cannot see the images Stratt actually executes.** An EE-Job image is named by an **Actuator in
the estate**, selected per Step (ADR-0117 D3a), and reaches the cluster through the dispatcher:

```go
image := spec.Image
if image == "" {
    image = d.cfg.EEImage
}
```

That is the whole of it. No validation. Two different sets of images, and this was the unvetted one —
the one that runs playbooks against production hosts with brokered credentials mounted.

### Why not an admission controller, which is the obvious answer

ADR-0165 D6 booked "in-cluster admission verification (a policy controller refusing unsigned images
at deploy)". Reaching for one here would mean a new cluster-wide dependency to check a property
Stratt already knows, at a layer Stratt does not own, with its own failure story for a registry
outage — against §1.4's "dependencies: few, boring".

**The dispatcher is where Stratt itself causes a pod to exist.** Enforcing at that point needs no new
component, covers exactly the images this platform is responsible for, and fails with a message that
names the Actuator's own declaration rather than a controller's generic rejection.

A cluster-wide controller answers a broader question — "what may run here at all" — and remains
booked. This answers "what may **Stratt** run", which is the part Stratt can actually promise.

## Decision

### D1 — The dispatcher refuses an EE-Job image pinned by TAG

When the estate requires digests, an image without `@sha256:` is refused **before a pod exists**, and
the error names the image and the reason (§1.8).

### D2 — The FALLBACK image is checked too, not just the Step's

A Step naming no image still runs something — `d.cfg.EEImage`. Exempting it would be the exemption
nobody remembers, on the code path taken by every Step that does not override the default, which is
most of them.

### D3 — One declaration governs both doors

`supplyChain.requireDigests` gates the chart (ADR-0168) **and** reaches the dispatcher as
`STRATT_REQUIRE_IMAGE_DIGESTS`. Two switches would be two things to forget, and an estate that gated
its control plane while running unpinned playbook images would have the reassurance without the
property.

### D4 — Opt-in, for ADR-0168's reason exactly

Every estate and demo in this repo runs on floating tags by design. A default-on refusal breaks all
of them, and the fix everyone reaches for switches it off globally and forever.

### D5 — What this does NOT do

**A digest is not a signature.** Pinning proves the bytes are stable, never whose they are. Saying it
in three ADRs running is deliberate: it is the confusion this whole arc is most likely to cause.
Verification is ADR-0165's `task supply:verify`, and the refusal points there.

**Nothing stops another actor.** A cluster admin, an operator, or a mutating webhook can still run
whatever they like. This governs what Stratt runs — no more, and the ADR does not imply otherwise.

## Consequences

- **The images that execute playbooks against production are gated**, which was the larger of the two
  sets and the unguarded one.
- **No new dependency, no new component**, and the check is four lines at the point of creation.
- **A Run fails with a declaration error** when an Actuator names a floating tag in a digest-requiring
  estate — visible at the Run, which is where an operator is already looking (§1.8).

## Verification

- unit: a tagged image is refused, and the refusal names the image, the ADR and the reason;
- unit: the **fallback** image is refused too (D2);
- unit: with the flag off, dispatch is unchanged — the regression that matters, since every estate
  today is that case. Proven by the call reaching the cluster it does not have: a refusal returns
  early instead, and never gets there;
- unit: a digest-pinned image passes, or the refusal would be unsatisfiable and no estate could turn
  the gate on;
- chart: the flag reaches the daemon as `STRATT_REQUIRE_IMAGE_DIGESTS` — asserted by rendering the
  production profile, because a gate wired to a value nobody propagates is inert in the quietest
  possible way.
