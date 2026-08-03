# ADR 0160 — The same job, possibly a different hand: launch-time parity with AAP

- **Status:** **Accepted** (2026-08-03, steward) — **all five decisions implemented and
  live-proven** on kind, gated by `demo:scale-fleet` and therefore by E2E-1. Charter review by hand — this session's rules bar
  the subagent; §1.2/§1.6/§2.2/§2.4/§2.5 answered inline. **No new runtime dependency.**
- **Date:** 2026-08-03
- **Deciders:** steward
- **Charter sections:** §1.2 (desired state in Git), §1.6 (one capability, every surface), §2.2
  (CaC-only declarations), §2.4 (no implicit precedence), §2.5 (execution authz), §7.6 (strangler-fig)
- **Closes AWX-015**, deferred out of **ADR-0128 D4**. Builds on **ADR-0118** (declared launch
  inputs), **ADR-0028** (View-scoped execution authz), **ADR-0117 D2/D3a** (one dry-run mechanism;
  the image is the content boundary), **ADR-0024** (parametrized Views).

## Context

AWX/AAP job templates carry ~16 `ask_*_on_launch` booleans. Each says: this field MAY differ between
the template and a job launched from it. AWX's own rule is the important part —

> Only fields specifically configured to be prompt-able are allowed to differ from the template to
> the job.

— which is a **declared envelope**, not free-form override. That distinction is what makes this
tractable here at all: an envelope declared in Git and filled in at launch is precedence with
somebody's name on it, which is what §2.4 asks for. A launch that could differ from its declaration
in ways nothing declared is the thing §2.4 forbids, and AWX forbids it too.

### The standard is TASK parity, not field parity

Stratt replaces AWX/AAP; it is not obliged to BE them. The bar, stated by the steward and adopted
here as the rule this ADR is measured against:

> We have to be able to perform the same tasks with the same level of permissions and ownership that
> AAP provides. If that task or ownership moves in Stratt that is fine, but it has to be doable.

So each field below is audited as a TASK — "what is the operator trying to do?" — and answered with
where that task lives in Stratt, not with whether a same-named field exists. A 1:1 mapping is not
the goal and would in several cases be a worse product.

### What the audit found: most of it already works

`ResolveStepParams` substitutes the `{{.launch.x}}` namespace into a Step's **params**. Every
promptable ansible knob is already a param of `ansible.input.v8`. So the mechanism AWX-015 was
deferred to build **already exists and is in use** — `demos/network-device` binds
`route_prefix: "{{.launch.routePrefix}}"` today.

And the envelope is **stronger than AWX's**. AWX has a boolean per field: prompting is on or off,
and an on field accepts anything. ADR-0118's `inputs` is a typed, closed JSON Schema that applies
declared defaults and REJECTS an answer to a question the Workflow does not ask. That is the "and
then some": AAP can say *may this vary*; Stratt says *may this vary, of what type, within what
bounds, with what default*.

| AWX field | The task | Where it lives in Stratt | Verdict |
| --- | --- | --- | --- |
| `variables` | pass run-time values | declared `inputs` + `{{.launch.x}}` (ADR-0118) | ✅ same ownership |
| `limit` | narrow the target set for one run | `params.limit`, bindable | ✅ mechanism exists |
| `job_tags` / `skip_tags` | run a subset of tasks | `params.tags` / `skipTags` | ✅ mechanism exists |
| `diff_mode` | show per-task changes | `params.diff` | ✅ mechanism exists |
| `verbosity` | more output | `params.verbosity` | ✅ mechanism exists |
| `forks` | parallelism | `params.forks` | ✅ mechanism exists |
| `timeout` | connection timeout | `params.timeout` | ✅ mechanism exists |
| `scm_branch` | run from a different ref | `params.scm.ref` | ✅ mechanism exists |
| `job_type` (run/check) | dry-run this time | Run-level `DryRun` (ADR-0117 D2) | ✅ ownership MOVED, deliberately |
| `job_slice_count` | slice a large run | `LaunchParams.Slices` | ✅ ownership moved to the Run |
| `instance_groups` | choose execution locus | Sites/Cells (ADR-0032/0044) | ✅ ownership moved; AWX-008 declined the mirror |
| `labels` | tag the job for search | AWX-006, separate | — out of scope here |
| **`inventory`** | **run against a different target set** | **nowhere on the direct door** | 🔴 **D3** |
| **`credential`** | **run with a different credential** | another Step, via Git | 🟡 **D4** — doable, ownership shifted further than needed |
| **`execution_environment`** | **run with a different EE** | another Actuator, via Git | 🟡 **D4** — same |

**The façade under-reports all of it.** `mappers.go` hardcodes `ask_variables_on_launch: true`,
`ask_limit_on_launch: false`, `ask_inventory_on_launch: false` and omits the other thirteen. AWX
tooling READS those booleans to decide what to prompt for, so a migrated template loses its prompts
even where the mechanism works. That is a §7.6 strangler-fig failure: the front door lies about the
building behind it.

## Decision

### D1 — Task parity is the standard, and the audit above is the artifact

Parity questions are answered by task, and the answer names where the task lives, including when
ownership moved. "Stratt has no `ask_job_type_on_launch`" is not a gap; "a Stratt operator cannot
dry-run a Workflow" would be, and they can.

The table is kept in `docs/parity/awx-object-model.md` rather than only here, because it is a
question adopters ask continuously and an ADR is not where a living answer belongs.

### D2 — The façade DERIVES `ask_*_on_launch`; it never hardcodes them

For each promptable field, the façade reports `true` when the Workflow's Step binds that param from
the `{{.launch.*}}` namespace AND the Workflow declares a matching input. Both halves, because
either alone is a lie: a binding with no declared input is a token nothing can fill, and an input
nothing binds changes no behaviour.

**Derived, never a second declaration.** The alternative — a `promptable:` list on the Step — would
be a second home for a fact the binding already states, and the two would drift (§2.4). The binding
IS the declaration; the façade reads it.

This ships the nine ✅ rows as visible parity without any new launch capability.

### D3 — A View may be supplied at a direct launch, authorized against the SUPPLIED View

`POST /api/v1/workflows/{name}/runs` gains an optional `viewName`, exactly as the remediation door
already carries one. Actuation Steps that name no View of their own inherit it — the ADR-0151
mechanism, unchanged.

**The authorization is the whole design and it is not new.** `authorizeLaunch` already checks
`runner` on every actuation Step's View and already takes a default for the inherited case. A
supplied View is checked the same way, against what was supplied. You may run a recipe against any
View you could have launched it against directly, which is precisely AAP's rule (`use` on the
inventory) expressed in this repo's existing vocabulary.

**Why this is the one real gap.** A Stratt operator today cannot do what an AAP operator does
routinely: take a working recipe and point it at a different target set. The remediation door can;
the front door cannot. That is not an ownership move, it is a missing capability.

### D4 — Credential and EE: the estate declares a PERMITTED SET, the launcher chooses within it

Both are doable today by declaring another Step or another Actuator, so the TASK is not blocked. But
the ownership moves further than the task requires: in AAP a launcher holding `use` on a credential
picks it themselves; in Stratt they need an estate author and a Git round-trip. D1 permits an
ownership move; it does not oblige the largest available one.

So: an Actuator declaration may carry a permitted SET (`credentialRefs`, `images`) instead of a
single value. A launch selects one member; the selection is authorization-checked exactly as a
declared one is — `user` on the CredentialRef (§2.5), and membership of the declared set for the
image.

**This PRESERVES ADR-0117 D3a rather than overturning it.** D3a's claim is that the image is the
content boundary and a Step selects content by selecting an Actuator — not that the boundary is a
single value. A declared set is still the estate deciding what content is permissible; the launcher
decides only among things already reviewed. `eeImage` stays deprecated: this is not a free-form
image field returning by another name, and an image outside the set is refused.

**Deliberately NOT `ask_credential_on_launch`'s full semantics.** AWX lets a launcher supply any
credential of a type they can use, including ones the template's author never contemplated. Here the
set is declared. That is a smaller envelope, and it is the one that keeps "what a Step may use" a
property of the estate rather than of whoever launched it.

### D5 — What stays declined, and why

- **A launch differing from its declaration in ways nothing declared.** The envelope is the whole
  mechanism; without it this is an override field, which is §2.4's implicit precedence with extra
  steps.
- **`instance_groups`** — AWX-008, declined: execution locus is Sites/Cells, and the mapping is not
  a field to copy.
- **`extra_vars` as an untyped bag when the Workflow declares inputs.** Already the case
  (`resolveLaunchParams`), and it stays: a typed envelope that can be bypassed by an untyped one is
  not an envelope.

## Consequences

- **Migrated templates keep their prompts** — the strangler-fig door stops under-reporting, which is
  the concrete cutover win and needs no new capability.
- **One new launch capability** (D3) and one new declaration shape (D4). Both are additive; every
  existing Workflow behaves identically.
- **The `ask_*` booleans become derived state**, so they cannot drift from behaviour. A Workflow that
  stops binding an input stops advertising the prompt, in the same commit.
- **Adopters get a stricter envelope than they had.** A survey question that accepted anything in
  AWX now has a type. That is a migration cost and it is the right direction; ADR-0118 already made
  it for variables.

## Verification

Not shippable on assertion — the standing rule here, and this arc has paid for it five times.

- Unit: `ask_*` derivation for a Workflow that binds `limit`, one that binds nothing, and one that
  declares an input it never binds (all three must differ), falsified by hardcoding.
- Unit: a launch supplying a View the Principal holds no `runner` on is REFUSED, and the refusal
  names the View.
- **Live**: a demo assertion that a launch-supplied `limit` ACTUALLY NARROWS the target set — the
  Run touches fewer hosts, observed from the Run's own per-target results, not from the request.
  "The envelope holds" is exactly the class of claim that has been false when executed.

### Landed so far (2026-08-03) — D1, D2, D3; NOT D4; NOT the live proof

**D2** derives all sixteen `ask_*` booleans from the declared inputs and their bindings. Three used
to be hardcoded, two of them permanently false. Unit-proven across declared-and-bound,
bound-not-declared, declared-not-bound, nested (`scm.ref`), the non-launch namespaces, and the union
across a DAG's Steps.

**Found while implementing, and preserved rather than resolved:** the two surfaces DISAGREE about
`ask_variables_on_launch`. `job_templates` merges untyped extra_vars when a Workflow declares no
inputs, so its door always accepts them; `workflow_job_templates` ties them to the survey and is
false without `inputs`. Both are shipped and both are tested. Unifying them is a behaviour change on
a compat surface and belongs in its own decision — it is written into `prompts.go` rather than left
for the next reader to rediscover.

**D3** accepts `viewName` on the direct launch door, and the grant is checked against WHAT WAS
SUPPLIED. The body is now decoded BEFORE the authorization call, and a guard pins that ordering:
authorizing first and reading the body after would check the grant on a View the caller did not name.
The remediation door REFUSES a supplied View — its View comes from the Baseline, and two sources for
one fact would need a precedence rule (§2.4).

**D4 landed after the rest, in that order deliberately** — the prompt stayed false until the
mechanism behind it existed. A launch may now select a `credentialRefs` SUBSET of what the Steps
declare, and an `image` from the Actuator's declared `images`. Both are membership-checked at the
DOOR, before a Run exists, so an unpermitted choice is a 400 naming the set rather than a Run that
dies in a pod.

**Both gates survive, which is the whole point.** The estate still bounds what a Step may EVER use
(membership), and §2.5's `user` check still runs per surviving credential in `ResolveCredentials`,
unchanged. What moved is only WHO picks among things an author already blessed — which is AAP's own
shape, where an admin enables prompting and RBAC bounds the choice.

**The dangerous case is the empty one, and it is tested:** an absent selection means "no selection
made", NOT "mount nothing". Every launch before this sends none, and reading it as an empty set would
silently strip every credential from every existing Run.

### The live proof, paid (2026-08-03)

`demos/scale-fleet` builds three hosts, so a narrowing is observable rather than asserted.
`fleet-limit` declares `hostLimit` and binds it into `params.limit`; the demo counts the DISTINCT
hosts each Run produced a per-target result for, read from the execution pod's own event stream
rather than from the request that started it. `task demo:scale-fleet:run` EXIT=0:

```
launch with hostLimit=all reached 3 host(s)
launch with hostLimit=<one host> reached 1 host(s)
✓ 3 → 1: the declared envelope actually bounded the Run
✓ a View the Principal cannot run is REFUSED (403), checked against what was supplied
✓ …and the granted View is accepted, so that refusal is a check and not a blanket no
✓ /api/v2 advertises ask_limit_on_launch=true, derived from the binding
✓ …and ask_inventory_on_launch=true, because the Step inherits its View
```

```
✓ a launch may NARROW to a declared credentialRef
✓ …and may NOT reach outside it, even for a ref the Principal holds `user` on
✓ an image outside the Actuator's declared set is REFUSED — D3a holds
```

The 403 is the half that matters most: `web-servers-restricted` selects the SAME hosts under a name
nobody holds `runner` on, so a pass there would mean the check ran against the Step's own View — or
against nothing. And the granted View is accepted immediately after, because a check that refuses
everything is not a check.

**D4's refusal is chosen with the same care.** `cert-issuer` is a real CredentialRef this Principal
DOES hold `user` on, and it is still refused — because it is not declared by this Workflow. A pass
there would have meant the §2.5 grant alone decided and the estate's per-Step bound counted for
nothing. "May I use it" and "may this Step use it" are different questions, and the estate owns the
second.

**Found by running it, and it constrains this shape: an "optional" prompt cannot be an empty
default.** The first `fleet-limit` defaulted `hostLimit` to `""`, and every launch failed
`ResolveStepParams` with `/limit: minLength: got 0, want 1`. The contract is RIGHT — an empty
`--limit` is meaningless — and the consequence is general: **a bound param must satisfy its
Actuator's contract on every launch**, so an optional prompt needs a default that MEANS the
unnarrowed case (here ansible's own `all`), not an absent one. Any future promptable field has to be
checked against its own contract's floor, and this is the reason.
