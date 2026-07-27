# ADR 0139 — A Step may run a Workflow: nesting, and the one chokepoint

- **Status:** **Accepted** — steps 1–4 implemented 2026-07-27. Step 5 (vocabulary-linter over the new
  identifiers) is a review action, not code. Charter review by
  hand; §1.6/§1.8/§2.4/§2.5 answered inline. **No new dependency, no new Named Kind.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.6 (one launch, one authz, one audit stream), §1.8 (the abstraction must never
  hide diagnosis — one-click descent), §2.3 (Workflow: "…convergence, **nesting**"), §2.4 (no implicit
  precedence), §2.5 (the launching Principal rides every credential use check)
- **Reconciles with:** **ADR-0011 (nesting was DEFERRED, NOT DROPPED — this resumes it)**, ADR-0031
  (cross-Step output binding), ADR-0118 (launch inputs + the shared `ResolveLaunchInputs` chokepoint),
  ADR-0122 (change context admitted at the door), ADR-0110/0114/0135 (`Provisions`/`Decommissions`/
  `Remediates` — the per-kind maps a nested Step resolves through), **ADR-0138 D2 (this is the missing
  primitive it depends on)**

## Context

ADR-0138 D2 found that capability resolution exists at the Intent, Actuator and Blueprint-route layers
but not at the **Step**, and scoped the fix as "ADR-0135 D3's shape, one layer down." That was wrong,
and the reason is worth stating precisely because it is what this ADR exists for:

**A provider's per-kind maps resolve to a WORKFLOW, not an Action.** `awsec2` advertises
`provisions: {Compute: compute-build}`, and it is `compute-build` that contains
`action: awsec2/create-vm`. So a capability-typed Step must invoke a **nested Workflow** — and
`types.Step` has no such form. Its dispatch switch has exactly four arms: `gate`, `policy`, `action`,
actuation.

The estate already feels this. `vsphere-subnet-build`'s first Step is commented
_"the **ipam capability's** allocate Action, composed as an explicit Step"_ — an author naming a
provider while describing a capability, because describing it is all the model allows.

Charter §2.3 has always listed nesting in the definition of a Workflow, and **ADR-0011 deferred it
explicitly** — _"Deferred, not dropped: cross-Step output binding and input Contracts …, Workflow
nesting, convergence, policy Gates…"_. Everything else on that list has since shipped. This is the
remainder.

### What already exists, and what does not

| already shipped                                                                                        | missing                                          |
| ------------------------------------------------------------------------------------------------------ | ------------------------------------------------ |
| Temporal child workflows per Step (`RunAction`, `RunActuation`)                                        | a Stratt Workflow invoking a Stratt Workflow     |
| Four places core launches a Workflow **by name** (Trigger, remediation, `Provisions`, `Decommissions`) | a **synchronous, in-DAG** launch                 |
| `Workflow.Inputs` — a JSON Schema launch interface (ADR-0118)                                          | `Workflow.Outputs` — there is none               |
| `WorkflowRun` with Principal, Cell, TriggeredBy                                                        | any **parent link** — nesting is unrepresentable |
| A launch **chokepoint** — `RunDAG` — that no transport can skip (ADR-0118 D4) | a caller that starts one as a CHILD |

That last row is the constraint everything else bends around, and it is already solved — for a launcher
that is not an HTTP handler. The **trigger engine** launches Workflows today without touching the HTTP
door: it builds a `DAGInput`, leaves `WorkflowRunID` empty, and starts `RunDAG`, which mints the
execution row itself via `EnsureWorkflowRun` (ADR-0018, "the ADR-0010 pattern"). `RunDAG` then calls
`ResolveLaunchInputs` for everyone —

> the same `contract.ResolveLaunchInputs` the RunDAG chokepoint calls — one implementation, two call
> sites: this one for the error message, that one **because no transport can skip it** (ADR-0118 D4).

— so the HTTP door's own validation is a courtesy that produces a good 400, not the guarantee. **The
guarantee lives in `RunDAG`.**

## Decision

### D1 — A nested Step starts a child `RunDAG`, exactly as a Trigger does

No new launch path, and no extraction: the nested Step builds a `DAGInput`, leaves `WorkflowRunID`
empty, and starts `RunDAG` as a Temporal **child** workflow. `EnsureWorkflowRun` mints the row;
`ResolveLaunchInputs` runs inside `RunDAG` as it does for every other launcher.

This is the trigger engine's pattern, unchanged, and choosing it over "extract the HTTP door's body" is
the difference between reusing the guarantee and re-implementing it. §1.6 is satisfied structurally
rather than by discipline: a nested Step **cannot** skip input resolution, because the thing it starts
is the thing that resolves.

It also means the parent Step's wait is an ordinary `ExecuteChildWorkflow` — the same mechanism
`runActionStep` already uses for `RunAction`.

**Authorization is part of what must be reused, and it is NOT inside that chokepoint.** `authorizeLaunch`
is a separate function each HTTP door calls first; it walks the Workflow's Steps and requires `runner` on
every `view:` they name, and its own comment says it is "shared by both launch doors so remediation
cannot become a softer path to the same execution." A nested Step that only started `RunDAG` would
inherit the launch sequence **minus authz** — the §1.6 asymmetry this decision exists to prevent,
reproduced by the mechanism meant to prevent it.

The hole is bounded but real. `CheckExecutionGrant` still fires per actuation Step inside the child with
the inherited Principal, so a nested run **cannot execute** against a View its launcher lacks `runner`
on. What is lost is the FAIL-FAST refusal: instead of a 403 naming the View, the launcher gets a
mid-tree `ExecutionDenied` on a child run. That is a §1.8 diagnosis regression at the exact moment of
denial, which is the moment it costs most.

So the same predicate runs before a child starts:

- **concrete form** — the parent's `authorizeLaunch` traverses statically into the named child's Steps
  at the door, so the 403 arrives before anything runs;
- **class form** — the child is unknown until resolution, so the resolving Activity runs the predicate
  against the inherited Principal before starting the child.

### D2 — A nested run IS a WorkflowRun, with a parent link

`WorkflowRun` gains `ParentWorkflowRunID` and `ParentStepName`. A nested execution is a first-class
WorkflowRun, not an inlining of the child's Steps into the parent DAG.

Inlining is the tempting alternative and it is wrong on three counts, each of which already works on
WorkflowRun and would have to be rebuilt: **descent** (§1.8's ladder is Intent → Workflow → Run → task
event; a resolved Workflow that appears nowhere breaks the rung), **Gates** (listed and approved per
WorkflowRun), and **audit**. It would also erase the child's own `Inputs` contract, which is the whole
point of it being a Workflow.

Cancellation is deliberately NOT on that list: there is no Workflow-level cancel today. The only
surface is `POST /runs/{id}/cancel`, per-Run, and ADR-0011 booked a Workflow-level cancel that never
shipped. Which makes the child's close policy a decision this ADR must take rather than inherit:

> **A nested child starts with `ParentClosePolicy: REQUEST_CANCEL`, never the default TERMINATE.**

Temporal's default terminates children when the parent closes. A terminated `RunDAG` never reaches
`finishWorkflowRun`, so its WorkflowRun row stays `running` forever and its K8s Jobs go unreaped — a
record that lies about a run's state, which is the §1.8 failure mode in its purest form. `REQUEST_CANCEL`
lets the child write its own terminal status, keeping the single-writer rule that already governs Run
status.

The parent link is what makes the tree navigable — without it a nested run is an orphan whose existence
is only inferable from timing.

### D3 — The Step names a Workflow **or** a capability; resolution happens once, at launch

```yaml
- name: provision
  needs: [approve]
  workflow: compute-build # the concrete form
  inputs: { … }
```

```yaml
- name: provision
  needs: [approve]
  workflowCapability: provisioning # the class form (ADR-0138 D1)
  forKind: Compute #   resolves through the provider's `provisions` map
  inputs: { … }
```

Mutually exclusive, refused at declaration if both are set (§2.4 — a silent winner between them is
exactly the implicit precedence the anti-GPO axiom forbids). Resolution runs in an **Activity** before
the child starts, so the Workflow stays deterministic and everything downstream — input validation,
dispatch, descent — sees a **concrete** Workflow name.

**This diverges from ADR-0138 D2 and the divergence is stated rather than glossed.** ADR-0135 D3 and
ADR-0138 D2 both resolve into a COMPILED artifact — "a rebind recompiles" — and a raw Workflow has no
compile pass, so this resolves at LAUNCH. Launch-time resolution forfeits the readable record compile
time gives, and this repo already argues against itself there: a thing "resolved at run time with no
declaration anyone can read afterwards" is the worse boundary.

So the nested WorkflowRun **records the resolved provider and the capability-binding that decided it**.
Without that the answer to "why did this run `compute-build`?" exists only in an Activity's history.

Fail closed on PENDING/AMBIGUOUS, carrying the resolver's own reason (§1.8): "no verified provider" and
"two providers, add a capability-binding" send the reader to different places.

### D4 — Inputs are checked against EVERY candidate; outputs are deliberately not bound

`compute-build` declares `required: [instance, projectKind, labels]`. So a nested Step must supply
inputs — and with the **class** form, _which_ Workflow (and therefore which input schema) is unknown
until resolution.

This is ADR-0135 D3's `remediationParams` problem exactly, and it takes the same answer:
**validate the Step's `inputs` against every CANDIDATE provider's Workflow at declaration time, not
against the winner.** Which provider wins depends on runtime state Git cannot see, so inputs that fit
only one of them break the other on a capability-binding change — the moment nobody is looking.

**Outputs are NOT bound back into the parent DAG in v1, and that is a design choice rather than a
deferral.** The estate's own flagship case shows why: `linux-onboard`'s `configure` Step does not read
`provision`'s outputs — it targets `viewName: linux-fleet`. The coupling is **the graph** (§1.2: a
Syncer projects the built host, the View picks it up), which is the architecturally preferred path and
does not need a data channel between Steps. Adding `Workflow.Outputs` before something needs it would
be a Contract demanded by no shipping consumer (§1.1).

### D5 — Cycles refused at declaration; depth capped

Workflow→Workflow edges form a static graph in the estate: a cycle is detectable at load and refused
there, naming the ring. With the class form the edge set is every candidate the class could resolve to
— so a cycle possible through _any_ binding is refused, for D4's reason.

Depth is capped with a stated constant. Unbounded nesting is unbounded Temporal child depth, and the
failure mode is a floor that stops accepting work for reasons no declaration explains.

### D6 — Principal, environment, change context and Cell are INHERITED, never re-asserted

The nested run carries the parent's Principal (§2.5 — "the launching Principal rides every Step's
credential use check"), the floor's active environment (ADR-0122 D2 — the floor's own, never a caller's
claim), the parent's change context, and the parent's Cell.

Stated as a decision because it is a privilege decision: nesting must not become a way to run something
as an identity the launcher does not hold. A child that could re-assert any of these would be a
confused-deputy channel wearing a Workflow's clothes.

### D6b — The parent Step takes the child's terminal status verbatim

A nested run's terminal status IS the parent Step's status: `failed`, `denied` and `expired` all make
the parent Step fail. ADR-0011 D5's "terminal status is raw" then holds transitively, and `needs:` +
`when: success | failure | always` keep meaning over a nested Step exactly as over any other. Left
unstated, those edges are undefined the moment a Step is a subtree — and a DAG whose conditions are
undefined for one node shape is a DAG nobody can reason about.

### D7 — A Gate inside a nested run belongs to that run

Gates are approved where they live. No Gate is hoisted or proxied — hoisting would put an approval on a
record whose inputs the approver cannot see, which is the opposite of approve-what-you-see
(ADR-0047 §7).

Discoverability has two routes, not one: `ListGates` filters by status and not by WorkflowRun, so a
nested run's pending Gate already appears in the global approval inbox; the parent link adds the second
route, finding it by walking down from the parent.

**What the class form does NOT give you** is knowing WHICH approvers a run will require before it
starts — the Gate set arrives with the winning provider. D4 already establishes "check every candidate"
for inputs, and the same rule applies here: a plan/declaration-time render must show every candidate's
Gates, or a route's approval requirements are only knowable after it launches.

## Charter alignment

- **§1.6.** D1 is this discipline stated as an implementation constraint: one launch, one authz, one
  audit — so the second caller must reuse the first's sequence, not resemble it.
- **§1.8.** D2 and D7 exist entirely for descent. A resolved Workflow that executed but appears nowhere
  in the ladder is hidden mechanism, and a pending Gate nobody can find is hidden diagnosis.
- **§2.3.** Nesting has been in the definition of a Workflow since the charter; this is not new surface.
- **§2.4.** D3's mutual exclusion and D5's cycle refusal are both "no silent winner."

## Consequences

- **ADR-0138 D2's gap has THREE Step shapes, and this closes ONE of them.** A Step names a `workflow:`,
  an `action:`, or an `actuator:` + `viewName:`. Nesting gives a class form to the **Workflow-shaped**
  one only, because that is the shape `Provisions`/`Remediates`/`Decommissions` resolve to. Measured
  against the two Workflows still stuck in `estate/`:

  | leg                                              | shape             | closed here?                         |
  | ------------------------------------------------ | ----------------- | ------------------------------------ |
  | `linux-onboard` provision — `awsec2/create-vm`   | Action → `compute-build` is Workflow-shaped | **yes** — the worked example |
  | `linux-onboard` converge — `ansible-platform-baseline` | Actuator + View | no — and ADR-0138 D5 blocks it besides |
  | `vsphere-subnet-build` — `netbox/ipam-resolve`   | Action (`ipam` is a class-level ACTION contract) | no |
  | `vsphere-subnet-build` — `vcenter/create-portgroup` | Action          | no                                   |

  **So neither Workflow moves on this ADR alone**, and saying otherwise would repeat exactly the mistake
  ADR-0135 D3 made: shipping a route whose flagship case it could not actually serve. The
  **Action-shaped half remains open** and is its own decision; the ansible converge leg additionally
  needs ADR-0138 D5 (EE-Job providers are permanently unverifiable today), which is a hard prerequisite
  for any route that resolves to ansible.
- **A Workflow becomes composable**, which is what §2.3 always claimed. Today "provision then converge"
  can only be written by naming a provider.
- **No new launch path.** The chokepoint gains a caller, not a bypass, because the caller starts the
  chokepoint itself.
- **`WorkflowRun` grows two columns**, and the parent link must land in the **API read** in the same
  slice — not in the UI later. Every capability is exposed identically to UI, CLI, CI and agents
  (§1.6/§6), so a tree a human can descend and an agent Principal cannot is a broken promise, not a
  backlog item.

## Alternatives considered

- **Inline the child's Steps into the parent DAG.** Rejected (D2): rebuilds descent, Gates,
  cancellation and audit, and erases the child's `Inputs` contract.
- **Let the Step call the child's Action directly, skipping the Workflow.** This is what
  `linux-onboard` does today (`action: awsec2/create-vm`), and it is the thing being fixed: it names a
  provider, and it bypasses whatever the provider's build Workflow does around the Action.
- **Add `Workflow.Outputs` now.** Rejected for v1 (D4): no shipping consumer demands it, and the
  flagship case couples through the graph instead. Add it when a Workflow needs a value the graph
  cannot carry.
- **Hoist nested Gates to the parent run.** Rejected (D7): an approval on a record whose inputs the
  approver cannot see.
- **Resolve the capability at estate-parse time.** Rejected: verification is runtime state, so a
  parse-time answer is stale by construction. Resolve at launch, exactly as the compiler resolves at
  compile.

## Implementation — steps 1–2 done; 3–5 outstanding

1. ~~**`WorkflowRun` parent link** (D2).~~ **DONE (2026-07-27).** Migration 00045 adds
   `parent_workflow_run_id` + `parent_step_name`, with a CHECK enforcing **both or neither** (a
   half-written link reads as navigable and is not) and no foreign key (retention may prune the
   parent while the child's record is still the audit trail — a dangling id is a dead end, a cascade
   is evidence destroyed). Exposed on the API in the SAME slice, per the Consequences: a tree a human
   can descend and an agent Principal cannot is a broken promise, not a backlog item.
2. ~~**The concrete form.**~~ **DONE.** `step.workflow` + `inputs`, a fifth Step shape exclusive with
   the other four. Cycle refusal names the **ring**, not just the fact; depth capped at a named
   constant (5). Input NAMES and `required` are checked always — static facts about two declarations —
   while VALUES defer when templated, the same split every params validator here makes.

   The runtime test runs the child **for real** rather than through `OnWorkflow`, and it had to: the
   child IS `RunDAG`, so mocking that type intercepts the parent too. That turned out to be the
   stronger test — it caught the fixture passing an input the child did not declare, refused by the
   child's own `ResolveLaunchInputs`. **The nested Step gets no exemption from the chokepoint**, which
   is D1's whole claim, demonstrated rather than asserted.
3. ~~**The class form** (D3/D4).~~ **DONE (2026-07-27).** `workflowCapability` + `forKind`, resolved in
   an Activity, inputs checked against every candidate, cycles walked over the full candidate set.
   Resolution reuses the compiler's own `capability.Resolve` assembly, **exported rather than
   duplicated** — two resolvers that can disagree would make the estate mean different things
   depending on who is asking (§2.4).

   **A latent trap was found and fixed doing this**: `assembleProvisioningProviders` hardcoded
   `types.CapProvisioning` while taking a class-shaped question. Exporting it unchanged would have
   returned provisioning providers for *every* class asked about — resolving to a real, wrong
   Workflow rather than failing closed, which is the worst available outcome. The class is now a
   parameter.

   **Scope, stated rather than glossed:** the class form resolves through `provisions` ONLY. It
   cannot search the other per-kind maps without a verb it does not carry — **vcenter maps `Compute`
   in both `provisions` (vsphere-vm-build) and `decommissions` (vsphere-vm-teardown)**, so a search
   across maps is ambiguous for the most ordinary provider in the estate, and choosing between build
   and tear-down is not a tiebreak core may make. Extending to the other verbs is a separate
   decision.
4. ~~**Convert `linux-onboard`'s PROVISION leg.**~~ **DONE.** It now reads
   `workflowCapability: provisioning` + `forKind: Compute`. Two things this proved rather than
   assumed: the D4 check really ran against **both** shipped Compute builders (awsec2's
   `compute-build` and vcenter's `vsphere-vm-build`), and it passed because they already declare the
   **same** required interface — which is the convergence that makes a class form viable here at all.

   Also note the old Step did not merely name a provider: `action: awsec2/create-vm` **bypassed
   `compute-build` entirely**, and with it the projection + correlation-label work that makes a build
   resolvable by the next reconcile.

   **The ADR's own caveat about the converge leg is now STALE**: it says that leg is blocked on
   ADR-0138 D5, which has since shipped — ansible is attestable, so the block is gone and that leg is
   re-openable on its own merits (it is Actuator-shaped, so ADR-0140 D4's form, not this one).
5. **Run `vocabulary-linter`** over `workflowCapability`, `forKind`, `parentWorkflowRunId`,
   `parentStepName` before merge (CLAUDE.md's rule for new core-model identifiers). None is a §2-banned
   term, but the check is the rule.

### Traps

- **The child's Gate is not the parent's Gate.** If a nested Gate ever becomes approvable from the
  parent's record, D7 has been lost.
- **Convergence timing is NOT solved by nesting.** `linux-onboard` provisions a host and then converges
  a View that must contain it — that depends on a Syncer having projected it, which nesting does not
  guarantee. Convergence was deferred alongside nesting in ADR-0011 and stays deferred; do not let a
  green nested run be read as proof the graph caught up.
- **Do not add `Workflow.Outputs` to make one Step convenient.** The graph is the coupling; a data
  channel between Steps is a second path to the same fact (§1.2).
- **Depth and cycle checks must cover the CLASS form's full candidate set**, not the currently-bound
  provider. A cycle that appears only after a capability-binding change is still a cycle.
- **`ParentClosePolicy` defaults to TERMINATE, and the default is wrong here.** A terminated child never
  writes its terminal status, so the row reads `running` forever. This is one line and it is invisible
  until a parent is cancelled in anger.
- **Do not let step 4 become "move the Workflow."** Only the provision leg is closed here. Converting the
  converge leg without ADR-0138 D5 would re-run the ADR-0135 D3 failure exactly — a route resolving to
  an EE-Job provider that can never be verified, green in every unit test because they resolve through
  a fake.
