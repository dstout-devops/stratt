# ADR 0141 — The plugin SDK is Go; the PORT is the contract, not a language

- **Status:** **Accepted** (2026-07-27, steward)
- **Date:** 2026-07-27
- **Deciders:** steward
- **Charter sections:** §1.4 (boring spine), §1.5 (sovereign contracts, multiple transports), §1.7
  (evergreen), §3 (tech stack), §7.2 (contributor demographics)
- **Amends:** **charter §3** (the "one supported plugin-SDK language" commitment) and **charter §1.7**
  (its "plus Python inside execution pods and the plugin SDK" parenthetical)
- **Supersedes in part:** **ADR-0002** — only its clause (b), "one supported plugin-SDK language, so
  Ansible-community contributors are not excluded". ADR-0002's substance stands untouched: the control
  plane is Go, Python is confined to execution pods, storage is S3-generic.

## Context

Charter §3 says Python survives in two places: inside execution pods, **and "as one supported
plugin-SDK language, so Ansible-community contributors are not excluded."** §1.7 repeats it.
ADR-0002 §(b) is where it came from.

That commitment was made in a world that no longer exists. It dates from the era when the backend
itself was Python — FastAPI, Django — so "the SDK is Python too" was continuity rather than a choice.
ADR-0002 removed the premise when it moved the control plane to Go; the SDK clause was carried
forward unexamined.

**The repo has since answered the question in practice, and the answer is unambiguous:**

| claim                                   | reality                                                                                           |
| --------------------------------------- | ------------------------------------------------------------------------------------------------- |
| a supported Python plugin SDK           | **does not exist.** `sdk/` holds `stratt/plugin/v1`, `mockstratt`, `secretbroker`, `mcp` — all Go |
| Python is a language Stratt ships       | **two first-party `.py` files**, `ee/content.py` + its test, both inside the EE image             |
| `pyproject.toml` is the SDK's packaging | its own header: _"deliberately NOT a distributable package — it is repo tooling config"_          |
| plugins are written in the SDK language | **twenty first-party plugins, all Go**                                                            |

A charter clause that has never been built, has twenty counterexamples shipping, and rests on a
premise the project discarded is not a commitment — it is a fossil. Charter §1.7's own words are that
the platform must never become "the Django-monolith fossil its predecessor did"; leaving this in place
keeps a Django-era assumption in the document that exists to prevent them.

### What the clause was actually protecting

Two things worth keeping, and neither needs Python:

- **"Ansible-community contributors are not excluded."** They are not, and never were by this. An
  Ansible contributor's contribution is **tool content** — playbooks, roles, collections — which is
  the `ansible` plugin's `contentDir` tree, not a Connector implementation. That path is untouched.
- **A plugin author needs a usable surface.** They have one, and it is stronger than an SDK: the
  sovereign port is gRPC + protobuf (ADR-0046), so **any language with a protobuf toolchain can
  implement a plugin.** §1.5 already says this — "our connector contract is our own; REST/gRPC,
  subprocess, and MCP are transports beneath it."

## Decision

### D1 — The supported plugin SDK is Go

`sdk/` is the plugin SDK, and it is Go: the generated port (`sdk/stratt/plugin/v1`), the
plugin-facing conformance host (`sdk/mockstratt`), and the in-pod secret resolver
(`sdk/secretbroker`). This states what already shipped rather than proposing anything.

One language for control plane, agent, and plugin SDK is the §1.4 argument ADR-0002 made for the
control plane, applied to the surface that grew around it: shared types, one toolchain evergreen gate,
one set of idioms for a contributor to learn.

### D2 — The PORT is the contract; a language is not

**A plugin is conformant because it speaks the port, not because it imports an SDK.** The port is
protobuf over gRPC, plus the EE-Job subprocess transport for tools that cannot be linked (§3's GPLv3
boundary). Both are language-agnostic by construction.

So the charter commits to **no** plugin-author language. It commits to a wire contract, which is
strictly more open: a Rust, Python, or TypeScript plugin that satisfies the port is a first-class
plugin today, with no charter change and no SDK to wait for.

The Go SDK is a **convenience over the port, never a gate to it.** `sdk/mockstratt` is the test of
that claim: it lets a plugin author exercise the governance seam without running a control plane, and
an author who does not use it is not thereby non-conformant.

### D3 — Python's remaining home is execution pods, and only that

Charter §3's first clause stands and narrows to the whole truth: Python lives **inside execution-
environment images** — the `ansible-runner` shim and `ee/content.py` — which is the GPLv3 subprocess
boundary. A separate process in a separate image, never linked into the control plane. `uv` there,
evergreen ≥ N-1 there.

That is not a grudging exception; it is the boundary that keeps Ansible usable without contaminating
the module graph, and it is load-bearing.

### D4 — A second SDK is a demand-driven decision, not a standing promise

If a community shows up wanting a Python (or any) SDK, building one is an ordinary decision made
then, on evidence. What changes here is that the charter stops **promising** one in advance. A
promise nobody is delivering is worse than no promise: it reads as a commitment to newcomers and as
debt to maintainers, and it has been neither for the entire life of the project.

## Charter alignment

- **§1.4 (boring spine).** One language across control plane, agent, and SDK is fewer moving parts
  than two. Deleting a surface that was never built is the boring choice.
- **§1.5 (sovereign contracts, multiple transports).** This is §1.5 applied to authorship: the
  contract is ours and the transports sit beneath it, so conformance is a wire property. Naming a
  language would put a _toolchain_ where a _contract_ belongs.
- **§1.7 (evergreen).** One less runtime line to hold at N-1. Python stays on the matrix for the EE
  image, which is where it actually runs.
- **§7.2 (contributor demographics).** ADR-0002's own argument — "the Argo/Crossplane/NATS
  maintainers this CNCF-track platform courts write Go" — applies to plugin authors at least as much
  as to the control plane, and the port keeps the door open for everyone else regardless.

## Consequences

- **Charter §3 and §1.7 are edited**, which is why this ADR exists: the charter is the design
  authority and supersedes every other document, so the document must change rather than be worked
  around. §3's Python sentence narrows to execution pods; §1.7's parenthetical drops "and the plugin
  SDK".
- **`.claude/rules/backend-python.md` narrows** to the execution-pod case, and `CLAUDE.md`'s
  restatement of §3 follows the charter.
- **Nothing in the code changes.** That is the point — this closes a gap between the document and a
  reality that settled long ago, and the absence of a diff outside prose is the evidence.
- **ADR-0002 stays Accepted.** Only its clause (b) is superseded; a reader arriving at it must be
  able to see that without reading this one, so it gains a pointer.
- **`pyproject.toml` keeps its comment** describing itself as repo tooling — now accurate rather than
  aspirational, since it no longer sits beside a promised SDK it was never part of.

## Alternatives considered

- **Build the Python SDK.** Rejected: it would be built for a charter sentence rather than a
  contributor, and §1.1's discipline — a schema exists because a shipping consumer demands it —
  generalizes. No one has asked.
- **Leave the clause and treat it as aspirational.** Rejected: the charter is the design authority,
  not a wish list. A clause the repo contradicts twenty times teaches readers that charter text is
  negotiable, which is the property that makes §1 worth having.
- **Say "the SDK is Go" and stop there.** Rejected as under-stating: it would imply a Go plugin is
  the only conformant kind. D2's point is the opposite, and it is the more open position.
- **Drop Python entirely.** Rejected: `ee/content.py` and the `ansible-runner` shim are real, run
  today, and are the GPLv3 boundary. Removing the clause wholesale would misdescribe the repo in the
  other direction.
