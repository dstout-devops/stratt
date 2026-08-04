# ADR 0165 — There has never been a release to sign

- **Status:** **Accepted** (2026-08-04, steward) — **CI-proven**: all three control-plane images
  built, keyless-signed and verified against this workflow's own identity, publishing nothing. Charter review by hand — this session's rules bar
  the subagent; §1.4/§1.5/§1.7/§1.8/§7.3 answered inline. **No new runtime dependency** — the tools
  are CI-only.
- **Date:** 2026-08-04
- **Deciders:** steward
- **Charter sections:** §7.3 (supply chain), §1.5 (sovereign contracts, pinned + hash-verified),
  §1.8 (never hide diagnosis), §1.4 (boring spine), §1.7 (evergreen)
- **Diverges deliberately from ADR-0032** (cosign-signed Bundles, key-based, verified in-process) —
  see D2, which is the decision worth arguing about.
- **Respects ADR-0046** (a plugin image is its own CI unit; core CI must scale to hundreds).

## Context

Charter §7.3, verbatim:

> Signed releases (cosign), SBOM, SLSA provenance **from release one**; SECURITY.md + disclosure
> process; pinned-digest images; community-tier plugins sandboxed by default; MCP outputs screened…

The parity register carries this as *"P6 — cosign image signing + SBOM (syft) + SLSA provenance in
CI"*, which reads like three missing steps in an existing pipeline. Reading the repository instead of
the tracker turns up something more basic.

### There is no pipeline for them to be missing from

`.github/workflows/` holds exactly two files: `ci.yml` and `e2e-live.yml`. CI builds the
control-plane images so a spec change cannot silently break a Dockerfile — and then **throws them
away**. Nothing pushes to any registry. No workflow triggers on a tag. There is no release job, no
registry, no published artifact of any kind.

So "from release one" is unmet in a way the tracker's phrasing hides: **there has never been a
release.** Signing, SBOM and provenance are all attestations *about a published artifact*, and the
gap is upstream of all three.

### What DOES exist, and it is the interesting half

**Verification is real, and it is better than most projects'.** `core/internal/bundle/verify.go`
reproduces cosign's verification **in-process** — no cosign CLI, no exec surface (ADR-0032, a
dependency-scout call) — and its tests hard-refuse a wrong key, a wrong pinned digest, an unsigned
Bundle and a tampered content layer.

That is the shape of the finding: **the platform verifies signatures it has never produced for its
own artifacts.** The discipline exists and stops exactly at the boundary where this project's own
releases would be.

Two smaller §7.3 obligations are also open, and one is free:

- **`SECURITY.md` does not exist.** §7.4 (OSPO/IP clearance) is now cleared, so it may be written.
- **Pinned-digest images are supported but not enforced.** `_helpers.tpl` prefers a digest over a tag
  and the chart WARNS on a floating tag; nothing refuses one.

## Decision

### D1 — The release is the unit, and it is a digest

A tag-triggered workflow builds the control-plane images, publishes them **by digest**, and every
attestation attaches to that digest. Tags are how humans find a release; the digest is what is
signed, attested and deployed (§1.5's "pinned and hash-verified", applied to our own artifacts).

Plugin images stay in their own CI units (ADR-0046) and follow this same shape rather than being
folded into a core pipeline that must scale to hundreds of them.

### D2 — Released images are signed KEYLESS, and that DIVERGES from ADR-0032 on purpose

Sigstore keyless: an OIDC identity from the workflow, an ephemeral Fulcio certificate, a Rekor
transparency log entry. Verification asks for the identity, not for a key:

```
cosign verify ghcr.io/<org>/strattd@sha256:… \
  --certificate-identity-regexp '^https://github.com/<org>/stratt/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**The consistency argument was considered and rejected, and recording that is the point of this
section.** ADR-0032 verifies Bundles against a pinned public key, in-process, deliberately without
the cosign CLI. Matching it here would be the tidier story and the weaker decision:

- **A long-lived private key in CI secrets is a worse trust root than an ephemeral identity.** This
  project has no HSM. A key in GitHub Secrets attests only "someone who had the key signed this";
  keyless attests "built by *this workflow*, in *this repo*, at *this ref*" — a claim a consumer can
  actually check, and one that survives the key we do not have to protect.
- **They are different artifacts with different consumers.** A Bundle is pulled by an agent at an
  edge Site, possibly air-gapped, and verified by a Go verifier we ship. A released image is consumed
  by the public and by other people's CI. Forcing one mechanism onto both optimizes for a symmetry
  nobody benefits from.
- **The SLSA half is keyless anyway.** `actions/attest-build-provenance` is identity-based by design,
  so key-based signing would mean maintaining two mechanisms to avoid maintaining one.

**The honest cost:** offline verification is harder. It is not impossible — the Sigstore trust root
can be cached and shipped, and attestations verified with their tlog entry bundled rather than
fetched — but it is more moving parts than a pinned key. Where offline verification genuinely
dominates is the edge-Site Bundle path, and that path **keeps** its key-based verifier. ADR-0032 is
not superseded; it is scoped.

### D3 — SBOM and provenance are attestations on the same digest, not files in a release page

An SBOM in a downloads folder describes whatever the person who uploaded it had. An attestation is
bound to the digest and verifiable by anyone who pulls it. Both the SBOM (syft, SPDX) and the SLSA
provenance attach to the published digest and are checked the same way the signature is.

### D4 — The pipeline VERIFIES ITS OWN OUTPUT, and fails the release if it cannot

The step after publishing pulls the digest back and verifies signature, SBOM and provenance. A
release that cannot be verified **fails** rather than shipping.

This is the §1.8 shape applied to ourselves: the failure mode being designed out is a signing step
that silently no-ops — a `continue-on-error`, a misnamed output, a permissions change — leaving a
green pipeline and unsigned artifacts that nobody notices until somebody tries to verify one. The
same check ships as `task supply:verify` so an adopter runs exactly what CI ran.

### D5 — Publishing is a SEPARATE, EXPLICIT act; this ADR wires the pipeline and does not push

The workflow ships with a **dry run** that builds, computes the digest, signs, attests and verifies —
**publishing nothing**. Keyless signing does not require a registry, so the trust machinery is
exercised in full before any artifact exists in public.

Flipping to real publication is a small reviewed change, and it is gated on a decision that is not a
coding task: the repository is not public yet, and a published digest can be deleted but never
un-fetched.

### D6 — What is declined or booked, each for a stated reason

- **In-cluster admission verification** (a policy controller refusing unsigned images at deploy) —
  **booked, not built.** It is the natural consumer of this work and a separate decision: it changes
  what a cluster will run, needs its own failure story for a registry outage, and belongs with the
  §7.3 "community-tier plugins sandboxed by default" clause rather than with producing releases.
- ~~**Enforcing pinned digests in the chart** — booked.~~ — **paid** by
  [ADR-0168](0168-a-warning-is-not-a-gate.md): `supplyChain.requireDigests` makes an unpinned image a
  render failure, opt-in via `values-production-supply-chain.yaml` for exactly the reason booked here
  (every dev floor uses floating tags deliberately). The in-cluster half stays booked.
- **Signing plugin images in this workflow** — declined. ADR-0046 makes each plugin its own CI unit
  precisely so core CI does not grow with plugin count. They follow this shape in their own pipelines.

## Consequences

- **§7.3 becomes partly true rather than aspirational**, and the part that remains false is named:
  nothing is published yet, so "signed releases" means "a proven pipeline with nothing through it".
- **A new external trust dependency at verify time** (Fulcio/Rekor) for image consumers — accepted in
  D2, with the offline path recorded rather than assumed.
- **`SECURITY.md` and a disclosure process exist**, which is a §7.3 obligation that needed no
  pipeline and was simply absent.
- **The keyless half cannot be proven on a workstation.** It needs an OIDC identity that only the CI
  environment has. That boundary is stated in Verification rather than blurred.

## Verification

Not shippable on assertion. This ADR owes:

- local: the SBOM and digest steps run and produce a real SPDX document for a real image;
- local: `task supply:verify` **refuses** an image whose signature is absent — the falsification that
  matters, because a verifier that passes everything is the exact failure D4 exists to prevent;
- local: `SECURITY.md` exists and names a disclosure channel and a response window;
- **CI**: one `workflow_dispatch` dry run that keyless-signs, attests and verifies, publishing
  nothing — the only place the identity half can be exercised, and it is owed before this ADR is
  Accepted rather than waved through as "it will work in Actions".

### Paid locally (2026-08-04); the CI half is still owed

**The SBOM is real.** `task supply:sbom IMAGE=stratt/strattd:dev` produced a 138-package SPDX-2.3
document naming the actual dependency graph (`github.com/jackc/pgx/v5`, `github.com/nats-io/nats.go`,
…) — generated from a LOCAL image, which is what makes the dry run possible at all.

**`supply:verify` refuses, and refuses for the right reason.** The first falsification attempt was
weak: an image that does not exist fails on access, which proves nothing about signatures. Pointed
instead at `ghcr.io/sigstore/cosign/cosign:v3.1.2` — an image that IS genuinely signed, by somebody
else — it refuses on IDENTITY:

```
expected SAN value to match regex "^https://github.com/dstout-devops/stratt/\.github/workflows/release\.yml@",
got "keyless@projectsigstore.iam.gserviceaccount.com"
```

That is D2's argument made executable: the gate checks WHO signed, not merely that something did. A
key-based check could not have failed this way, because a valid signature by the right key is the
whole of what it can assert.

### And the CI half, paid (2026-08-04)

Run `30868609634`, tag `v0.0.0-dryrun.1`, all three images **success**:

```
packages: 138
signed the digest as a blob — identity-bound, nothing pushed
✓ verified: signed by this workflow's identity      Verified OK
```

Keyless signing works end to end — the OIDC exchange, the Fulcio certificate and the Rekor entry —
and the verification step confirms the signature against the workflow identity rather than merely
noting that signing exited zero. **Nothing was published**: `PUBLISH` is `false` for any tag push by
construction, so the entire trust chain ran against images that existed only in the runner.

### What the dry run cost, which is worth recording

**The tag triggered `e2e-live.yml` as well**, because that workflow also fires on `v*` — so proving a
signing pipeline started the whole six-runner live demo suite. It was cancelled within a minute and
the tag deleted, but the lesson generalizes: **`v*` is a shared trigger namespace in this repo, and a
"harmless" tag is not harmless.** A future dry run should use `workflow_dispatch` from the default
branch, which is what D5's `publish` input is for and costs one job instead of seven.

### What building it found

- **Every action pin I wrote from memory was wrong in some way.** All five SHAs resolved to real
  commits — and one (`anchore/sbom-action`) was labelled `v0.20.2` while actually being `v0.20.6`,
  with `v0.24.0` current. Two others were several majors behind. Every pin is now resolved from the
  API and comments match the tag they name. A supply-chain pipeline whose own pins are guesses is
  the joke that writes itself.
- **`syft:v1.34.0` does not exist.** The version was invented; 1.50.0 is current. The same applied to
  cosign (guessed 2.6.1; 3.1.2 is current). Both are pinned in `Taskfile.yml` beside the other gate
  tools, for the reason stated there: a gate whose thesis is "unpinned things resolve differently on
  different days" must not itself float.
