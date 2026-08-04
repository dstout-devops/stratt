# ADR 0170 — From a definition to an image

- **Status:** **Proposed** (2026-08-04, steward). Charter review by hand — this session's rules bar
  the subagent; §1.4/§1.7/§1.8 answered inline. **No new dependency** — `ansible-builder` is
  deliberately not adopted.
- **Date:** 2026-08-04
- **Deciders:** steward
- **Charter sections:** §1.4 (boring spine — few dependencies), §1.7 (evergreen), §1.8 (never hide
  diagnosis)
- **Completes [ADR-0124](0124-ee-content-is-pinned-declared-and-verified.md) D1**, which built the
  compatibility front door and stopped one step short of a build. Enforces
  [ADR-0159](0159-a-transport-fails-on-three-axes.md)'s third axis at the declaration boundary.

## Context

The parity register carries P5 as *"no `ansible-builder` / `execution-environment.yml` factory; a
single hand-written `ee/Dockerfile` (compat asserted, not automated)"*. **That is stale**, and
reading the tree rather than the tracker narrows the gap to something specific.

What already ships (ADR-0124 D1): `task ee:factory:requirements EE=…` reads an AAP
`execution-environment.yml`, resolves `dependencies.galaxy` — inline or by path, ansible-builder's
own semantics — and prints a requirements document. Sections that cannot carry
(`additional_build_steps`, `additional_build_files`, `images`, `options`) are **named on stderr**,
never silently dropped. `dependencies.python` and `dependencies.system` are reported the same way.

So the declaration is read, and the content is installed by a pipeline that byte-pins and
hash-verifies — which `ansible-builder` itself does not do.

### The gap, stated precisely

**Nothing turns that reading into an image.** The output is a YAML document on stdout; the operator
then hand-assembles a content file, picks build args, and runs `docker build`. "Compat asserted, not
automated" is wrong about the reading and right about the build.

And one reported-not-carried section is worse than the others:

**`dependencies.python` is ADR-0159's third axis.** That ADR was written because an EE passed a
collection check and still could not connect — `ansible.netcommon` declares no hard SSH transport, so
the collection installed and no python module came with it, and the Run died at connect time naming a
module the estate never mentioned. `dependencies.python` is exactly where an AAP operator declares
that module. Today it is printed as a caveat, and `EE_PYTHON_EXTRA` — the build arg ADR-0159 added
for precisely this — sits in `ee/Dockerfile` unwired to any declaration.

An operator migrating an EE whose definition says `dependencies.python: [ansible-pylibssh]` gets a
warning and an image that cannot connect.

## Decision

### D1 — One command from a definition to a built image

`task ee:factory:build EE=path/to/execution-environment.yml TAG=…` reads the definition, materializes
the requirements, resolves the python extras, and builds — with the same pinned, hash-verified
content pipeline every other Stratt EE uses.

The declaration is AAP's. The **build** is ours, and that split is ADR-0124 D1's rule applied one
step further: compatibility lives at the declaration boundary, not in adopting somebody's build graph.

### D2 — `dependencies.python` CARRIES, into `EE_PYTHON_EXTRA`

The mechanism already exists; this connects it to the declaration that means it.

**This is the axis that decides whether the EE can connect at all** (ADR-0159), so leaving it as a
printed caveat while every other declared dependency is installed was the wrong side of the line.
`dependencies.system` stays reported-not-carried: an apt package lands in a layer whose position and
provenance are the Dockerfile's business, and there is no ADR-0159-shaped failure behind it.

### D3 — `ansible-builder` itself is NOT adopted, and that is a considered refusal

It is Apache-2.0 and the real tool, so this is not a licensing dodge:

- **It would not build what we need.** Its output does not byte-pin content or verify a lockfile;
  ADR-0124's whole argument is that "install `community.general`" is not a build input, and
  ansible-builder is content with exactly that.
- **It is a second build graph** with its own base-image opinions, layer ordering and options —
  §1.4's line, and a thing to keep evergreen (§1.7) for a job our Dockerfile already does.
- **The compatibility adopters need is the DECLARATION**, not the builder. They have an
  `execution-environment.yml`; what they want is an image that installs what it names.

### D4 — Sections that cannot carry still REFUSE to be silent

`additional_build_steps` and friends remain named on stderr, and the build proceeds without them —
unchanged from ADR-0124 D1. A definition leaning on a build step gets told which part did not come
across, which is the §1.8 property this whole path was written for.

## Consequences

- **An AAP operator's existing definition builds**, which is what P5 was actually asking for.
- **The python axis stops being a footnote**, closing the exact hole ADR-0159 was written about.
- **`ansible-builder` stays out of the toolchain**, so nothing new needs an N-1 floor (§1.7).
- **The parity row needs rewriting**, not ticking: most of P5 shipped in ADR-0124 and the register
  never noticed.

## Verification

- unit: a definition with inline `dependencies.galaxy` and one with a `galaxy:` **path** both yield
  the same requirements document — ansible-builder's own two spellings;
- unit: `dependencies.python` reaches the build as `EE_PYTHON_EXTRA`, in declaration order, and is
  **absent** from the caveat list it used to appear in;
- unit: `dependencies.system` is STILL reported and still not carried (D2's deliberate asymmetry —
  a test that would fail if someone "fixed" it for symmetry);
- unit: `additional_build_steps` is still named on stderr;
- **live**: `task ee:factory:build` against a definition declaring a collection AND a python module
  produces an image whose manifest reports both — the ADR-0159 three-axis check run against an
  image built from an AAP definition, which is the whole claim.
