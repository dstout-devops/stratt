# ADR 0124 — EE content supply: an `execution-environment.yml` front door, and an offline source that is verified the same way

- **Status:** **Proposed** (2026-07-25, steward). Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.4/§1.7/§2.5/§7.3 answered inline. **No new runtime dependency is added** — see D1,
  which is the call a dependency review would have focused on.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.4 (boring spine), §1.5 (pinned + hash-verified), §1.7
  (evergreen), §1.8 (never hide diagnosis), §2.5 (secrets brokered, never baked), §3 (Ansible is
  subprocess-only), §7.3 (supply chain)

## Context

Two ADR-0117 follow-ups, batched because they are the same seam from two ends: **(a)** the
`ansible-builder`-compatible EE factory (parity P5 — "this ADR defines the contract it must satisfy"), and
**(b)** air-gap content seeding, which ADR-0117 named twice as its largest unsolved gap and which
`enterprise-readiness.md` PLG-1 still leans on.

What ships today (ADR-0117 D3, follow-up i): content is declared in a **real Galaxy
`requirements.yml`**, version-pinned, installed at EE **build** time, verified against the resolved set,
and byte-pinned by `ee/content/<name>.requirements.lock.json`. That is a stronger guarantee than
`ansible-builder` itself offers — ansible-builder resolves content at build time with **no byte-pinning at
all**, so two builds of the same definition can legitimately produce different bytes.

Which sets up both decisions:

- **(a) is an input-format problem, not a build-engine problem.** An operator arriving from AAP has an
  `execution-environment.yml`. What they need is for it to work here — not for us to adopt their builder
  and lose the lockfile.
- **(b) is a source problem, and the pin machinery already solves the hard half.** The reason air-gap is
  hard is normally "how do I know I got the right bytes offline?" — and that question is already answered
  by a lockfile that verifies the installed tree. What is missing is only a way to install from something
  other than Galaxy.

### What already ships that this must reconcile with

| Machinery                                                          | Where                           | Bearing                                                       |
| ------------------------------------------------------------------ | ------------------------------- | ------------------------------------------------------------- |
| Pin enforcement **before** the network is touched                  | `content.py:install` → `verify` | The offline path must not weaken this ordering                |
| Per-artifact lockfile over the **installed tree**                  | ADR-0117 (i); `_tree_digest`    | The whole reason an offline source can be trusted             |
| `_check_source` — no credentials in a declaration                  | `content.py:94`; §2.5           | An offline source must not become a new place to bake a token |
| Content is a **build-time** declaration; the image digest is truth | ADR-0117 D3                     | Neither half may introduce run-time content resolution        |
| Relocking is a deliberate, reviewable act, never part of a build   | ADR-0117 (i)                    | `pull` must not silently relock                               |
| `EE_CONTENT` / `EE_LOCK_MODE` build args                           | `ee/Dockerfile:89`              | Where a third arg lands                                       |
| An S3-compatible object store already ships                        | ADR-0093 (SeaweedFS/Garage)     | The obvious artifact home — and deliberately **not** required |

## Decision

### D1 — Accept `execution-environment.yml` as an input format; do NOT adopt `ansible-builder` as the engine

The factory's contract is **compatibility of the declaration**, not adoption of the tool. `content.py`
learns to read an `execution-environment.yml` (ansible-builder schema v3) and extract its
`dependencies.galaxy` — whether that is inline requirements or a path to a `requirements.yml` — then feeds
the existing pin → install → resolved-set → lockfile pipeline unchanged.

**Why not just run `ansible-builder`.** Three reasons, in order of weight:

1. **It would lose the lockfile.** `ansible-builder` has no byte-pinning; handing content resolution to it
   would trade our strongest supply-chain property (§7.3) for convenience. An EE we produce must remain
   reproducible in the sense ADR-0117 (i) established.
2. **It is a second build engine for one artifact.** `ee/Dockerfile` already produces the EE, bakes the
   `stratt-ansible` shim, and holds the `/runner` contract. Two paths to one image is the §2.4-adjacent
   "which one is live?" problem, and the one nobody runs rots.
3. **It is a new build dependency** for a job a `Dockerfile` already does. §1.4 says boring spine; §1.7
   says every dependency is an upgrade liability.

**What compatibility therefore means, stated precisely so it is falsifiable:** an operator's
`execution-environment.yml` names content we install at the same versions, and the resulting image
satisfies the same `/runner` contract — so an `ansible-builder`-produced EE stays a **drop-in
replacement** for ours, which is the §3 commitment. It does **not** mean we execute their build graph:
`additional_build_steps`, custom base images and `python`/`system` dependency sections are **read and
reported, not silently ignored** (§1.8) — a definition using them tells the operator exactly which parts
did not carry over rather than producing an image quietly missing them.

### D2 — An offline source is a **local artifact directory**, verified by the lockfile that already exists

`content.py install` gains `--artifacts <dir>`: when set, collections and roles install from tarballs in
that directory (`ansible-galaxy` installs from a local path natively) and **nothing reaches Galaxy**.

The pin check still runs first, and the lockfile check still runs after — unchanged, and that is the whole
argument. An offline install is trustworthy here for exactly the reason an online one is: the digest of
the installed tree must match the committed lockfile. **Air-gap does not get a weaker guarantee than
online; it gets the identical one.**

`task ee:content:pull` populates that directory from a network-connected machine, and it is deliberately
**not** a relock: it downloads the declared versions and stops. Relocking stays the separate, reviewable
act ADR-0117 (i) made it, because a `pull` that relocked would launder the exact event the lockfile exists
to catch.

**The object store is where an operator SHOULD keep those artifacts** (ADR-0093 ships one), and it is
deliberately not required: the seam is a directory, so a bind-mount, a build context, an S3 sync, or a USB
stick all work. Coupling the EE build to an object-store client would put a substrate dependency inside
the image build for no gain (§1.4).

### D3 — Air-gap is now a **build-time** property, and the run-time story does not change

Stated because it is the thing an air-gap claim usually gets wrong. ADR-0117 D3 already made a Run never
need Galaxy — content is baked, and the image digest is the truth about what a Run had. D2 removes the
**build's** last network dependency for content. Nothing here introduces run-time resolution, and the
honest residual is unchanged from ADR-0117: the base image itself still comes from a registry, so a fully
air-gapped build needs a mirrored base image, which is an operator's registry concern and not something
this decision can fake.

## Charter alignment

- **§1.1 / §1.4.** No new format is invented and no second build engine is adopted; an existing
  third-party format is read, and the existing pipeline is reused.
- **§1.5 / §7.3.** The lockfile remains the byte-level authority on both paths. The offline path is
  verified by the same hash, not by trust in the medium.
- **§1.7.** Zero new dependencies; the evergreen surface is unchanged.
- **§2.5.** `--artifacts` is a directory, not a URL, so it cannot become a place to bake a credential —
  and `_check_source` continues to refuse a credentialed source in a declaration.
- **§1.8.** An `execution-environment.yml` section we do not carry over is REPORTED, never dropped
  silently.
- **§3.** `ansible-builder` compatibility is honoured at the declaration boundary; nothing GPL is linked,
  and no new tool enters the build.

## Consequences

- **Positive.** Closes both follow-ups. An AAP operator's existing EE definition is usable without
  rewriting it, and an air-gapped site can build content-bearing EEs with the same byte guarantee a
  connected one gets. Parity P5 becomes real rather than aspirational, and PLG-1's content half is
  genuinely closed (its reachability half is untouched and stays open — see ADR-0117 (e)).
- **Negative / trade-offs.** We read a format we do not own, so an upstream schema change is a
  maintenance event — bounded, because we read one section of it. `additional_build_steps` and custom
  base images are reported rather than executed, so a definition leaning on them needs operator work; that
  is stated at the point of use rather than discovered later. `pull` requires one connected machine, which
  is inherent to air-gap.
- **No follow-ups are booked.**

## Alternatives considered

- **Vendor `ansible-builder` and shell out to it.** Rejected in D1: loses byte-pinning, adds a second
  build engine and a dependency, for a job the existing Dockerfile does.
- **A private Automation Hub as the offline source.** Rejected as the primary mechanism: it is a service
  to run and credential (§2.5 exposure in a declaration COPYed into a layer) where a directory of
  verified tarballs needs neither. An operator who has a Hub can still point `requirements.yml` at it —
  that path is unchanged and orthogonal.
- **Resolve content at run time from the object store.** Rejected: it re-opens exactly what ADR-0117 D3
  closed, making a Run's content a function of what the store held at run time rather than of its image
  digest.
