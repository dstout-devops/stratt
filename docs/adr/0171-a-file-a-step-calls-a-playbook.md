# ADR 0171 — A file a Step calls a playbook

- **Status:** **Proposed** (2026-08-05, steward). Charter review by hand — this session's rules bar
  the subagent; §1/§1.4/§1.8/§3 (GPL boundary) answered inline. **No new runtime dependency** —
  ansible runs only as a subprocess in an image that already exists.
- **Date:** 2026-08-05
- **Deciders:** steward
- **Charter sections:** §3 (Ansible is subprocess-only; the control plane never links it), §1.4
  (boring spine), §1.8 (never hide diagnosis), §1 (no new configuration languages)
- **Pays ANS-013**, and **reframes it**: the register books it as a Step-level runtime gate
  (`docs/parity/ansible-tool.md:281`); this makes it build-time.

## Context

ANS-013 reads *"`--syntax-check`, `--list-tasks`, `--list-hosts` — cheap, and genuinely useful as a
pre-flight gate before a Run."*

Reading the code first narrows it considerably. **Playbook EXISTENCE is already validated at estate
load** — `core/internal/desiredstate/content.go:206-241`, param-name-agnostic via each Actuator's
declared `contentInputs`, refusing with the file and the content root's actual contents. A Step
naming a playbook that is not there already fails its file.

What is not checked is whether the file is a **playbook**. Core deliberately never looks:
ADR-0117 D6 and `content.go:199-200` — *"EXISTENCE ONLY. Core never opens the file and never parses
a play; variable-binding checks stay in TESTS, where tool awareness is allowed."* So a Step pointing
at a role task file, a vars file, or a play with a misspelled module is a **3 a.m. discovery**.

There is no `ansible-lint` and no `--syntax-check` anywhere in `Taskfile.yml`. Four playbook comments
across the estate assert the capability is "now possible"; none of them is executed by anything.

## Decision

### D1 — A REAL `ansible-playbook --syntax-check`, run as a subprocess in the EE image

Not a structural imitation in Go. ansible parses these files, because ansible is the only thing that
knows what a playbook is — and it catches a class no hand-written checker reaches, which the
falsification below demonstrates rather than asserts: **`couldn't resolve module/action`**.

**This is not a GPL problem, it is the GPL answer.** Charter §3: *"Ansible is subprocess-only: the Go
control plane never links it; it shells out to `ansible-runner` in the EE image."* The check runs
`docker run --entrypoint ansible-playbook <ee> --syntax-check`. Core still never opens a playbook
(ADR-0117 D6 intact) — this is a gate beside core, not a code path inside it.

### D2 — Build-time, not a Step-level gate, and the register's framing is refused

A dispatch-time check would put play parsing into the dispatcher, which ADR-0117 D3a already refused
for the neighbouring reason, and would move the failure to the moment a Run is already underway. The
whole value is moving it to review.

### D3 — The files are DERIVED from the estate's own reference graph, never globbed or listed

`tools/playbookrefs.py` reads every `actuators/*.yaml` with `pluginIdentity: ansible`, resolves its
`contentDir` against the estate root that shipped it, and collects the values of the params its
`contentInputs` names — the same declaration `validateContentRefs` uses to check the file exists.

Two things this refuses:

- **A hand-kept list of content roots** would be the second-copy defect `e2e:list` exists to prevent:
  someone adds an Actuator, forgets the list, and the playbooks are ungated while the gate reports
  success.
- **Globbing `*.yml`** under a content root would feed `group_vars/`, vars files and role task files
  to a playbook parser, producing failures that say nothing about the estate. The estate already
  states which files it calls playbooks.

Reading `contentInputs` rather than a param literally named `playbook` holds the same §1.4 line core
holds: the estate says which params are content, not this script.

### D4 — Each playbook is checked in an EE that RUNS it, and passes if ANY does

`--syntax-check` resolves module names, so the image matters: a play using `frr.frr` parsed by the
platform EE fails for a reason about the image, not about the play. Each Actuator declares its image
(ADR-0117 D3a), so the deriver emits it.

**A playbook may be referenced by several Actuators, and passing under one is enough.** This is not
leniency — it is the estate being right. `demos/network-device` points BOTH `ansible-network` (the
FRR EE) and `ansible-plain` (the platform EE) at `configure.yml`, because the second pairing exists
to be **refused**: it is the negative fixture proving ADR-0117 D3a's image gate fires.

`--syntax-check` conflates two questions — *is this play well-formed* and *does this image carry its
modules*. Only the first is ANS-013's. The second belongs to the image gate, which is already
live-proven, and an estate is allowed to declare a failing pair deliberately. Requiring every pairing
to parse would fail this estate for being correct.

### D5 — Its own CI job, so the cost is visible

`task ci` builds no EE image. Folding this in would add an EE build to every lint run; a separate job
named `Ansible syntax (ANS-013)` keeps that attributable. The three EE variants the estate declares
are built from `dev:ee*:build:image` targets — split out of the existing `dev:ee*:build` so CI (no
cluster) and the demos (cluster) share one build command rather than two copies of it.

### D6 — What it does NOT check

- **Module ARGUMENTS.** `--syntax-check` resolves the module and parses the play; it does not
  validate every argument against the module's spec.
- **Jinja binding** — `core/internal/desiredstate/unbound_playvars_test.go` owns that half.
- **Inline `params.play`** (ADR-0134 D4). Those are playbook documents that never touch a content
  root, so this gate does not see them. Booked, not solved.
- **A Workflow posted to the API**, which has no estate root in hand — the same limit
  `content.go:202-205` already states for the existence check.

## Consequences

- **A malformed playbook fails at review**, in a job named after the thing it checks.
- **A new EE build in CI** on every PR — accepted in D5, and visible.
- **Two new Taskfile targets per EE variant** (`:image` and the loader), which is one command each
  rather than two copies.
- **The estate's deliberate negative fixture keeps working** (D4), which a stricter gate would have
  broken.

## Verification

- `task ansible:syntax` green over the estate's referenced playbooks;
- falsified by a module nobody ships, by a file that is not a playbook, and by a deriver that reaches
  nothing;
- the CI job green on the PR.

### Paid (2026-08-05)

`task ansible:syntax` → **16 referenced playbook(s) parse, each in an EE that runs it.**

**Falsified three ways, and the first is the one that justifies using real ansible:**

```
✗ apache-configure.yml — [ERROR]: couldn't resolve module/action 'totally.not.a_module'.
✗ apache-configure.yml — [ERROR]: A playbook must be a list of plays, got a ...TaggedDict instead
✗ playbookrefs: no playbooks referenced by any Step … Refusing to report success.
```

A structural checker written in Go could have caught the second and the third. **It could not have
caught the first**, because knowing that `totally.not.a_module` does not exist requires the collection
set — which lives in the image.

**What the gate found on its first run, before any fixture was broken:** `configure.yml` failing
under `stratt-ee:dev`. Not a defect — the wrong-EE Actuator that `demos/network-device` declares
precisely so the image gate has something to refuse. That is what produced D4, and it is a better
argument than the one this ADR would otherwise have made: the first honest run of a new gate found
the estate right and the gate too strict.
