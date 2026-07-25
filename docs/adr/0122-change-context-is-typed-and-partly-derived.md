# ADR 0122 — The change context is typed, and the facts core can know are derived, not asserted

- **Status:** **Proposed** (2026-07-25, steward). Charter review done **by hand** (this session's rules bar the
  subagent); the §1.1/§1.4/§2/§2.4/§2.5 checks it would have run are answered inline under _Charter
  alignment_, including the one call that is genuinely contestable (D2 removes a field a launcher could set).
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.4 (content-blind spine), §1.6 (one capability, every
  surface), §1.8 (never hide diagnosis), §2 (frozen vocabulary, core-owned namespaces), §2.4 (no implicit
  precedence), §2.5 (authorization is not advisory), §7.3 (governance primitives)

## Context

Two ADRs booked follow-ups that turn out to be the same decision.

**ADR-0118 D4b** — the change context is untyped, and that is fail-open. `ChangeContext.Environment` and
`.ChangeClass` are bare strings assembled from whatever the launcher put in `DAGInput.Context`
(`policystep.go:assembleChangeContext`). So `environment: "prd"` produces a _different policy outcome_ than
`"prod"`: a freeze window keyed on prod (ADR-0067) simply does not match, and the change proceeds. A Control
keyed on `changeClass == "standard"` never fires against `"standrd"`.

The direction matters and the ADR-0118 note did not spell it out: **break-glass's own typo direction is
fail-CLOSED.** `policy.go:257` reads `cc.ChangeClass == "emergency"`, so a misspelled emergency leaves the
bypassed controls standing. That is why this has never bitten — the one place core already depends on the
literal happens to fail safe. Every _other_ use fails open, and there is no rule making the safe direction
the general one.

**ADR-0117 D1** — typed `become` is declared and audited but **not Control-gateable**, stated honestly at the
time: `ChangeContext` carries no Step params, so no Control can see `params.become`, and teaching the PDP to
read inside an ansible field is the `if ansible{}` §1.4 forbids. It booked "the plugin declares a typed signal
that the PDP gates on".

They are one decision because both ask the same question: **which facts about a change may the launcher
assert, and which must core establish?** Typing the asserted ones without answering that leaves a launcher
free to assert a fact core could have known — which is not a typo problem, it is an authorization problem.

### What already ships that this must reconcile with

| Machinery                                                                       | Where                                        | Bearing                                                             |
| ------------------------------------------------------------------------------- | -------------------------------------------- | ------------------------------------------------------------------- |
| `DAGInput.Context` split from `LaunchParams`                                    | ADR-0118 D4a                                 | The split is what makes typing expressible; this completes it       |
| `assembleChangeContext` — deterministic, no I/O, on the workflow goroutine      | `policystep.go:214`                          | Anything needing I/O must be stamped at launch, not assembled here  |
| Break-glass activation on `change_class == "emergency"` + incident + reasonCode | ADR-0070; `policy.go:257`                    | The class set is **already** load-bearing in core, just unvalidated |
| Environment scoping is a **boolean membership filter, never precedence**        | ADR-0057; `types/envscope.go`                | Environment is a property of the FLOOR, not of a request — see D2   |
| `Store.ActiveEnvironment()`                                                     | used by the reconcile controller             | Core already knows which environment it is                          |
| Refusing a core-owned label namespace from an untrusted supplier                | ADR-0120 (`stratt.intent/` in Action params) | The exact guard shape D3 reuses                                     |
| A closed, spine-owned enum whose members must argue membership                  | ADR-0120 D1 (`launchKind`); ADR-0121 D1      | The precedent for D1's set staying at three                         |
| Actuator CaC grants (`facetNamespaces`, `identitySchemes`)                      | ADR-0117 D3a; `types/actuator.go`            | Where a per-Actuator declaration belongs, and the precedent for one |

## Decision

### D1 — `changeClass` is a closed, core-owned enum: `standard | normal | emergency`

Core already depends on `emergency` (ADR-0070's activation) so the set is not new — it is being written down
and enforced. Validated wherever change context enters, and an unknown value is **refused**, not coerced: a
launcher asserting `"emergancy"` gets an error naming the valid set, rather than a Run whose governance
silently differs from what was intended.

Closed for the reason `launchKind` is closed (ADR-0120 D1) — these are spine acts, a plugin never adds one,
and a fourth class must argue membership rather than appear. `standard | normal | emergency` because those are
the three ADR-0070's contract already implies: an ordinary change, a routine-but-tracked one, and a declared
emergency.

### D2 — `environment` is DERIVED from the floor, and a launcher may not assert it

This is the contestable call, so the reasoning is explicit. ADR-0057 makes environment a **property of the
floor**: a daemon carries an active `STRATT_ENVIRONMENT` and reconciles only its slice. A Run executing on a
`dev`-scoped floor is in `dev` — that is not an opinion the launcher holds, it is a fact core already has
(`Store.ActiveEnvironment()`).

So `environment` leaves the asserted set entirely: it is stamped onto `DAGInput` at launch from the floor's
own value, and a launcher-supplied `environment` key in `context` is **refused**. Two things follow, and the
second is why this is the right shape rather than merely a tidier one:

- The typo hole closes **completely** for this field, without a core enum of customer environment names —
  which §1.1 would not allow anyway (core must not enumerate the world) and ADR-0057 deliberately leaves
  free-form.
- **A launcher can no longer choose its own policy environment.** Before this, a caller on a prod floor could
  assert `environment: dev` and walk past a prod freeze window. That is an authorization defect, not a
  data-quality one, and typing the string would not have fixed it. ADR-0118 D4a already removed the
  _accidental_ version of this (an input named `environment` reaching `ChangeContext`); this removes the
  deliberate one.

An unscoped floor (empty `STRATT_ENVIRONMENT`) yields an empty policy environment, exactly as today. That is
an operator configuration choice with a visible consequence — environment-keyed Controls do not match — and it
is stated rather than papered over with a default that would silently mean "prod" or "dev".

### D3 — `stratt.change/` is a core-owned label namespace, and `privileged` is derived from the declaration

This closes ADR-0117 D1's booked gating, content-blind.

**The reserved namespace.** `ChangeContext.Labels` is launcher-supplied. Core-derived facts must not share
that bag on equal terms, or a launcher spoofs them — so any `stratt.change/*` key arriving in `context` is
**refused** at the same chokepoint, the guard shape ADR-0120 already uses to keep `stratt.intent/` core's own.
§2 makes the namespace core's; this makes that structural rather than conventional.

**The derivation.** An Actuator declares which of its input paths elevate a change:

```yaml
name: ansible
elevatedInputs: [become.enabled] # ADR-0122 D3
```

Core walks each Step's params for those paths and, when one is present and truthy, derives
`stratt.change/privileged: "true"` onto the Run's change context. A Control gates on that label. **Core never
learns the word `become`** — it reads a declared path list and a boolean, which is the same content-blind
shape as `facetNamespaces` (core enforces a namespace ceiling without knowing what `os.hardening.sshd` means).

**Why the Actuator declaration and not the input Contract**, which is the better long-run home: the Contract
is the tool's own statement and would be the right place for it — but `ansible.input.v5` is **pinned and
hash-verified** (§1.5), so annotating it in place is blocking drift, and minting `v6` drags in ADR-0117 D2's
deferred removal of the deprecated `check`/`eeImage` fields. That is a separate decision, and this one does
not need it: the Actuator declaration is reviewed in Git, sits beside the other CaC grants it resembles, and
the mapping moves to the Contract whenever a v6 is minted for its own reasons.

**One class, not a taxonomy.** `privileged` is the only derived class, because it is the only one a shipping
Control needs (ADR-0083 D4's sufficiency rule). A second class argues membership.

## Charter alignment

- **§1.1 (type the seams).** `changeClass` is typed because a shipping Control consumes it; `environment` is
  _not_ given a core enum precisely because that would be typing the world.
- **§1.4 (content-blind spine).** D3's derivation reads a declared path list, never a tool's field name. No
  `if ansible{}` enters core.
- **§1.6 (every surface).** Validation sits at ADR-0118 D4's single chokepoint below all four launch paths, so
  the API, MCP and both Trigger doors reject the same values identically.
- **§1.8 (never hide diagnosis).** Every refusal names the offending key and the valid set. The unscoped-floor
  consequence is stated, not defaulted away.
- **§2 (core-owned namespaces).** `stratt.change/` joins `stratt.intent/` as a namespace the spine defends
  structurally.
- **§2.4 (no implicit precedence).** Derived facts do not _override_ asserted ones — asserting them is
  **refused**, so there is never a conflict to resolve by precedence.
- **§2.5.** D2 removes a spoofable input to an authorization decision; D3 adds a gate where one was
  documented as missing.
- **§7.3.** Break-glass keeps its activation contract; D1 makes its literal a validated member.

## Consequences

- **Positive.** Closes both booked follow-ups. Turns a documented gap ("`become` is not gateable") into a
  shipped gate, and turns a fail-open string into a refused one. Removes a launcher's ability to choose its
  own policy environment — the finding that made this worth doing rather than a typing chore.
- **Negative / trade-offs.** A launcher that _was_ supplying `environment` in `context` now gets an error;
  nothing in-repo does, and the refusal names the reason. `elevatedInputs` is a per-Actuator declaration an
  author must remember, and an Actuator that omits it derives nothing — honest, and visible in review, but not
  automatic. The mapping lives in the estate rather than the Contract until a v6 exists.
- **No follow-ups are booked.** Both halves ship whole. The Contract-versus-Actuator home for
  `elevatedInputs` is decided (Actuator, with the condition under which it moves), not deferred.

## Alternatives considered

- **A core enum of environment names.** Rejected: §1.1 forbids core enumerating the world, and ADR-0057
  leaves environments free-form deliberately.
- **Validate `environment` against the union of the estate's `environments:` lists.** Considered seriously and
  rejected as worse than D2: it closes the typo hole but leaves the launcher choosing among _valid_ values, so
  the authorization defect survives. It also needs declaration I/O inside a chokepoint that is currently pure.
- **Let core derive `privileged` by inspecting `params.become` directly.** Rejected — that is the
  `if ansible{}` ADR-0117 D1 named.
- **A new `ChangeContext.Capabilities []string` field.** Rejected as unnecessary sprawl: `Labels` already
  carries string facts a Control can gate on, and the reserved namespace makes a derived label
  untamperable. A second collection would need a rule about how it relates to labels.
- **A Step-declared `changeClass`.** Rejected: unenforceable. Nothing would compel a Step that escalates to
  declare it, so the gate would protect only the honest.
