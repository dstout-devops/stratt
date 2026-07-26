# ADR 0134 — A playbook is a playbook: tool content lives beside the estate, not inside a declaration

- **Status:** **Proposed** (2026-07-26, steward). **D1/D2 corrected the same day** — the first draft
  proposed a flat playbook folder; the unit is a **project** (a content root), for reasons the repo already
  documented. The error is kept on the record in D1 rather than edited away. Charter review by hand (this session's rules bar the
  subagent); §1.2/§1.4/§1.8/§2 answered inline. **No new dependency.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.4 (boring spine — the spine stays tool-blind), §1.8 (never hide diagnosis),
  §1.2 (projections), §2 (frozen vocabulary), §9 (no new language)
- **Reconciles with:** ADR-0025 (`params.scm` — the external-repo content ref), ADR-0033 (`packs/` — the
  in-tree content precedent), ADR-0051 (the EE Job `/runner` contract), ADR-0056 (estate as code),
  ADR-0117 D3a (**the pattern this generalises**: content selection is a declared property of the
  Actuator, never a params string) and D6 (core never parses play content), ADR-0118 (the parameter
  plane), ADR-0132 D4 (why an Actuator input Contract versions as a sibling)

## Context

Six estate Workflows, two demo Workflows and two Triggers carry **Ansible plays as inline YAML strings** —
about 240 lines of one language embedded in another. The repo has already paid for this twice, and both
receipts are in the tree:

- [`estate/workflows/access-apply.yaml`](../../estate/workflows/access-apply.yaml) opens with the
  post-mortem of a **shipped bug**: the play referenced three variables defined nowhere, because
  `{{ var }}` (Ansible) and `{{.ns.field}}` (Stratt) are _"visually identical in YAML and only one was ever
  checked."_
- [`core/internal/desiredstate/unbound_playvars_test.go`](../../core/internal/desiredstate/unbound_playvars_test.go)
  exists **solely** to re-check that seam by parsing plays back out of declarations.

That test is the tell. We wrote a parser to inspect a language embedded in another language, because the
embedding put the play beyond the reach of every tool that would otherwise check it: `ansible-lint`,
`ansible-playbook --syntax-check`, editor syntax highlighting, `molecule`. A playbook stored as a string is
a playbook nothing but us can read.

The operational cost is the one worth fixing: **supporting a Workflow currently requires holding both
languages at once**, in one file, with no visual boundary between them.

### What already exists, so this invents as little as possible

- `JobSpec.Files map[string]string` mounts arbitrary files into the EE pod at `/runner/<key>` — its own doc
  comment gives `"project/play.yml"` as the example. **Delivery is solved.**
- The shim already runs **a named playbook from `project/`**: that is exactly what the `params.scm` path
  does after cloning (ADR-0025). A mounted `project/` is the same shape as a cloned one.
- `packs/` is the precedent for in-tree tool content as reviewable data that is not a Named Kind.

### The objection to pre-empt

Someone will invoke ADR-0117 D3 — content is pinned at EE **build** time so a Run never depends on run-time
reachability. It does not apply. **A playbook already travels as estate today**, inline in the Workflow,
down the same git-sync/ConfigMap path; moving it to a sibling file changes which file it lives in, not its
trust path. D3's argument was about **registry** reachability (never assume Galaxy is up) and a repo-local
playbook has no registry. Collections and roles are third-party dependencies; playbooks are first-party
desired state, and they change with intent rather than with the toolchain.

So this is a **layout** change, not a supply-chain change.

## Decision

### D1 — Tool content lives at `estate/<tool>/`, and for Ansible the unit is a **project**

**This section originally proposed a flat `estate/ansible/playbooks/`. That was wrong**, and the evidence
against it was already in the repo — recorded here rather than quietly fixed, because the mistake is
instructive: it designed a content layout in isolation from the projection that has to describe it.

Ansible has three levels and the flat layout collapsed two of them:

| Level       | What it is                                                        | Already in the repo as                                                                 |
| ----------- | ----------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| **Ansible** | the binary; an EE image family                                    | the `ansible` Actuator, `ee/`                                                          |
| **Project** | a content root — playbooks, `roles/`, `group_vars/`, requirements | `projectID`, `STRATT_ANSIBLE_AUTOMATION_ROOT`, an AWX Project (`scm_url`+`scm_branch`) |
| **Org**     | a grouping of projects                                            | `ansible.org`, projected since ADR-0025                                                |

Three things break under a flat tree:

1. **Identity collides.** The content half qualifies every projected identity as `<projectID>/<relpath>`
   for the stated reason that _"two content roots' identically named files never collide in one estate."_
   Every Ansible repo has a `site.yml`.
2. **The cross-source join has nothing to join on.** The `runs` edge is built as
   `<project.name>/<playbook>`, and `controller/normalize.go` says what that means: _"the operator aligns
   `STRATT_ANSIBLE_AUTOMATION_CONTENT_ID` with the AAP Project name — the statement **this AWX Project IS
   this Git content root**."_ Flat content has no project name, so the edge ADR-0085's orphan Finding rests
   on cannot be formed from in-tree content.
3. **ADR-0127 D1 already settled the cardinality**: one plugin instance per Source, one content root per
   instance. N projects was already N content roots; a single folder contradicts a decision this repo
   shipped.

So the unit is the **project**, and a project is a content root:

```
estate/
  workflows/access-apply.yaml            ← the declaration: what runs, where, with which inputs
  ansible/projects/platform-baseline/    ← ONE content root == one AWX Project
    site.yml                             ← a playbook, lintable as one
    roles/
    group_vars/
    requirements.yml
  ansible/projects/web-content/          ← another; `site.yml` here is a different file
    site.yml
```

Inside the estate tree because that is already the delivery path for everything that decides what runs, and
because each demo already carries its own `estate/`, so `demos/app-cert/estate/ansible/projects/…` needs no
new convention. Scoped per tool so OpenTofu modules and Helm values get the same treatment later without a
second argument.

`ParseDir` ignores subtrees it does not own, so a tool directory is not a declaration and is never parsed
as one.

**In-tree is one source of a project, not the definition of one.** An AWX Project is literally an SCM
pointer, and `params.scm` (ADR-0025) already runs a named playbook from a cloned repo. A project can
therefore arrive in-tree, from external Git, or — eventually — from a published registry, while the thing a
Step selects stays the same. That convergence is not hypothetical: **`ansible.project` + `scm_revision` is
already booked as `AWX-001`** (ADR-0127 D4), to bind catalogue to execution and repair ADR-0085's
soundness. The self-service registry and AWX-001 are the same entity seen from two directions, and this ADR
deliberately does not pre-empt it — it only ensures the content layout is shaped so AWX-001 can land
without moving every file again.

### D2 — An Actuator **declares** its project; the spine copies it blind

`estate/actuators/ansible.yaml` gains `contentDir: ansible/projects/platform-baseline`. Everything under it
is mounted into the Job's `project/` directory.

**One Actuator per project**, which is not a new rule — it is the one that declaration already states: _"A
Step that needs different CONTENT names a different Actuator."_ Three things follow, and they are why this
beats teaching the Step to name a project:

- **Per-project grants.** `facetNamespaces` is a write ceiling, and it can now differ per project — a
  project with no business writing hardening facets does not get to.
- **Isolation.** Only the selected project's tree mounts, so a Run cannot reach another project's content.
  A Step naming a project inside its params would have mounted everything and relied on the Step to pick.
- **The review point for self-service.** A platform admin reviews the Actuator (its grant, its EE image);
  the team owns the files inside their project directory. That is the same separation of duties ADR-0035
  chose when it refused auto-teaming — the directory owner decides content, the platform decides authority.

This is not a new idea — it is the **third** declared-path field on that same declaration, and the other
two already state the principle:

- `image` (ADR-0117 D3a) — _"content selection stays a Git-reviewable property of the Actuator rather than
  a params string a tool passes at run time."_
- `elevatedInputs: [become.enabled]` — _"Core never learns the word `become`: it reads a declared path and
  a boolean, exactly as facetNamespaces lets it enforce a write ceiling without knowing what a namespace
  means."_

So core copies **a directory an Actuator declared**. It does not know what a playbook is, does not read one,
and gains no `if ansible {}`. Had the core instead resolved an ansible-specific `playbookRef` out of Step
params — the obvious first design, and the one this ADR started with — it would have put tool awareness
straight into the tool-blind dispatch path that D3a exists to keep clean.

**A whole directory, not one file, and that is deliberate**: a real Ansible project has `roles/`,
`group_vars/`, and playbooks that `import_playbook` each other. Mounting the tree means content authored as
Ansible keeps working as Ansible (§9 — we are not inventing a dialect that permits only single files).

### D3 — Resolved at estate-parse time, onto the Actuator

The desired-state engine reads `contentDir` when it loads the declaration and carries the files on the
Actuator, rather than dispatch reading a filesystem. Three reasons, in order of weight:

1. **Sites work unchanged.** `JobSpec.Files` is remote-safe (it is not `Env`, ADR-0032), so content
   travels with the JobSpec to a Site. A dispatch-time filesystem read would require the estate mount to
   exist wherever the Actuator runs, which for a remote Site it does not.
2. **A playbook change shows up in `stratt plan`.** Changing a playbook changes what will run, so it
   _should_ appear in the diff of desired state. Inline plays got this by accident; a reference would have
   lost it. This keeps it on purpose.
3. Dispatch stays filesystem-free, which is what makes it testable.

**Bounded, and loudly:** the mounted tree becomes a ConfigMap, which Kubernetes caps at 1 MiB. The load
refuses a `contentDir` that exceeds a conservative ceiling with a message naming the directory and the
size, rather than producing a Job that fails to schedule for reasons nobody can see (§1.8). Per-project
scoping (D1) makes that ceiling far more comfortable than the flat tree would have: a Run mounts **one**
project, not every project in the estate.

### D4 — The Step names a playbook; `ansible.input.v7`

The input Contract gains `playbook` — a path **within** the mounted tree. The shim reuses the branch it
already has for `params.scm`: run this named playbook out of `project/`. The only new behaviour is that
`project/` arrives mounted instead of cloned.

A **sibling version**, per ADR-0132 D4's rule: an Actuator input Contract is a wire promise to Step
authors, so it versions as `ansible.input.v7.schema.json` rather than widening v6 in place.

`params.play` is **kept, not deprecated**. A seven-line guard play (`vacuous-run-guard`) is clearer inline
than as a file in a directory, and pretending otherwise would trade one awkwardness for another. The rule
is: **if it is Ansible anyone would maintain, it is a file.**

### D5 — The checks stay in tests, and core still never parses a play

`unbound_playvars_test.go` keeps doing its job and gets **easier**: it reads real playbook files instead of
extracting strings from declarations, and it can now also assert that every Step naming a playbook names
one that exists. A test is allowed to be tool-aware; the runtime is not. ADR-0117 D6 — _core never parses
play content to discover what it needs_ — is preserved exactly, which is why the parse-time check is
existence (a path) and never content.

## Charter alignment

- **§1.4.** The spine copies a declared directory and learns nothing about Ansible. The alternative design
  is named in D2 precisely because it was tempting and wrong.
- **§1.8.** Failures move earlier (a missing playbook fails a test, not a 3 a.m. Run) and the ConfigMap
  ceiling fails with a cause instead of an unschedulable Job.
- **§9.** No new language and no dialect: content authored as Ansible stays valid Ansible, including
  multi-file layouts.
- **§2.** No new Named Kind. `contentDir` is a field on an existing declaration.

## Consequences

- **Positive — support stops requiring both languages at once**, which is the whole point. A playbook
  becomes lintable, syntax-checkable, highlightable and testable by the ecosystem's own tools, and a
  Workflow becomes a short declaration of what runs where.
- **The layout now matches the projection.** A directory under `estate/ansible/projects/` is the same
  thing the content half calls a content root and the same thing AWX calls a Project, so an in-tree project
  can carry a `runs` edge from a mirrored job template exactly as an external one does. Under the flat
  layout first proposed here, in-tree content could not have participated in that edge at all.
- **Two files instead of one** for a given behaviour. Real, and the trade the ADR accepts: the coupling
  moves from "inside a string" to "a named reference", which is the coupling every other estate reference
  already has.
- **Not a security boundary.** Mounting the whole tree means a Step can, in principle, name any playbook in
  it. That matches today (a Step can inline any play at all) and is bounded by the same Git review. If
  per-Step content restriction is ever wanted it is a separate decision, not something to imply here.
- **The `contentDir` ceiling is a real limit** an estate can grow into. Named in D3 rather than discovered.
- **Migration is mechanical but wide**: 10 files, and the demo estates and chart dev-declarations tree
  carry copies that must move together or the demos break. Each migrated play must also be **assigned to a
  project**, which is a judgement rather than a move — the reference estate's plays are currently one
  undifferentiated set because nothing ever made them choose.
- **An Actuator per project is more declarations**, and for a large self-service estate that is a real
  count. Accepted because each is small, each carries a grant worth reviewing individually, and the
  alternative concentrates authority instead of distributing it.

## Alternatives considered

- **A flat `estate/ansible/playbooks/` tree.** This ADR's own first proposal, corrected in D1 and left on
  the record. It collides on `site.yml`, gives the cross-source `runs` edge no project name to join on, and
  contradicts ADR-0127 D1's one-content-root-per-Source cardinality. The lesson generalises past Ansible:
  a content layout has to be shaped by the projection that describes it, not designed beside it.
- **Keep the tree flat but let the Step name a project** (`params.project`). Rejected with the same
  reasoning as the `playbookRef` alternative below — it needs core to read a tool-specific param — and it
  additionally mounts every project into every Run, so isolation depends on the Step behaving.
- **Core resolves a `playbookRef` from Step params.** The obvious design, and rejected in D2: it puts
  ansible awareness in the tool-blind dispatch path.
- **Keep plays inline; lean harder on the test.** The status quo. Rejected: the test exists because the
  embedding defeats every standard tool, and a bespoke parser will always trail the language it parses.
- **Bake playbooks into the EE image** (ADR-0124's factory). Strongest pinning, and rejected: it makes an
  operator's own playbook a build-time artifact, which is the opposite of the authoring ergonomics driving
  this — and it splits playbooks into two classes, shipped and estate.
- **A top-level `content/<tool>/` tree** beside `estate/`. Conceptually tidier — `estate/` stays purely
  Named-Kind declarations — but it needs a second delivery path wired through the chart, git-sync and every
  demo, to separate things that are always authored, reviewed and shipped together.
