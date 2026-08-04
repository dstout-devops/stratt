# ADR 0168 — A warning is not a gate

- **Status:** **Proposed** (2026-08-04, steward). Charter review by hand — this session's rules bar
  the subagent; §1.8/§7.3 answered inline. **No new dependency, no code change** — chart only.
- **Date:** 2026-08-04
- **Deciders:** steward
- **Charter sections:** §7.3 (supply chain — pinned-digest images), §1.8 (never hide diagnosis)
- **Pays the cheap half of [ADR-0165](0165-there-has-never-been-a-release-to-sign.md) D6**, which
  booked *"enforcing pinned digests in the chart… needs a values-gated production profile."*

## Context

§7.3 lists **pinned-digest images** among its supply-chain commitments. The chart has supported them
since ADR-0013 — `stratt.image` prefers `.digest` over `.tag` — and `NOTES.txt` prints:

> `!! image stratt/strattd:dev uses a floating tag — pin a digest in production (charter §7.3)`

**Nothing has ever failed because an image was floating.** A warning printed after a successful
`helm install` is read once, by the person who already decided to install. The commitment is real,
the mechanism is real, and the enforcement is a sentence.

This is the same shape ADR-0165 found one layer up — a verifier with nothing to verify — and the
same shape the `explode` and boundary gates keep surfacing: **a rule that exists and is not
checked is a rule that is not kept.**

## Decision

### D1 — `supplyChain.requireDigests` makes an unpinned image a RENDER failure

Turn it on and `helm template` refuses, naming every offending image:

```
supplyChain.requireDigests: 2 image(s) pinned by TAG, not digest:
  forwarder (stratt/stratt-forwarder:dev), strattd (stratt/strattd:dev).
A tag is MUTABLE — the bytes behind it can change after the review that approved them.
```

**`helm template` is the point of enforcement because it is the tool every install already runs** —
CI, GitOps and a human all pass through it. A gate somewhere else is a gate somebody can route
around.

### D2 — It is OPT-IN, and that is not timidity

Every dev floor and demo in this repo runs on floating tags **by design**: `pullPolicy: Never` with
kind-loaded images, rebuilt constantly. A default-on refusal would break all of them on day one, and
the fix everyone would reach for is to switch it off globally and leave it off — a control that is
worse than no control, because it reads as protection.

It ships as `values-production-supply-chain.yaml`: one flag, layered over real values, in the profile
where it means something.

### D3 — The gate is ONE pass over the images, not a check inside `stratt.image`

The natural-looking place is the image helper itself. It cannot work: that helper receives an image
dict, so it cannot see `.Values`, and threading the chart root through every call site would touch a
dozen templates to check one flag.

A single `stratt.requireDigests` partial, invoked once from a template that always renders, walks the
control-plane images and every declared plugin. Smaller, and auditable in one read — which is what a
supply-chain gate has to be.

### D4 — What this does NOT do, said plainly

**A digest is not a signature.** Pinning proves the bytes are stable; it says nothing about whose
bytes they are. The verification that answers that is ADR-0165's, and the profile's comment points at
`task supply:verify` for exactly this reason.

**Nothing is enforced inside the cluster.** A digest can be pinned in the chart and the cluster can
still run whatever a mutating admission controller or a manual `kubectl set image` puts there.
In-cluster admission verification remains booked (ADR-0165 D6) — it changes what a cluster will run,
needs its own failure story for a registry outage, and is a bigger decision than this one.

## Consequences

- **A §7.3 commitment becomes checkable** by the tool every install already runs.
- **Two new cases in `chart:lint`**, and the primary assertion is the FAILURE — see Verification.
- **No behaviour changes by default**; every dev floor and demo renders exactly as before.
- **The pinned path is now exercised**, which it was not: the chart supported digests and no profile
  in the repo used them, so the `@sha256:` branch of `stratt.image` had never rendered in a gate.

## Verification

- `chart:lint` **asserts the refusal**: rendering the production profile against the chart's own
  defaults — which are floating tags — must FAIL. A gate that rendered here would be one nobody had
  ever seen fire.
- `chart:lint` **asserts the acceptance** beside it: the same profile with digests set must render,
  or the refusal would be unsatisfiable and the profile unusable.
- Both directions confirmed: `2 image(s) pinned by TAG` refused, and a digest-pinned estate rendering
  `image: stratt/strattd@sha256:…`.
