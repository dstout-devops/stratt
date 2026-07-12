# ADR 0024 — Payload templating + parametrized Views

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1 (no-new-config-language non-goal), §1.5/§1.8, §2.1
  (Views), §2.3 (Step bindings), §4.2 (`{{.package}}`), §4.3 (blast radius),
  §5 Flow 3; ADR-0018 (the deferred payload-templating rider)

## Context

Two parked riders share one design: binding Emitter-event fields into a
Trigger-launched Run/Workflow's params (ADR-0018 deferred it), and
parametrized Views (a View whose selector binds a value at reference time —
Flow 3: an alert names the host, the remediation targets *that* host). The
governing constraint is the §1 permanent non-goal, **"no new configuration
languages."** The charter's only blessed binding is `{{.package}}` (§4.2, a
single explicit field reference) plus structured `outputs.instances[*]` →
"the next Step's View parameters" (§2.3).

## Decision

1. **One substituter, explicit field lookup only** (`internal/template`). A
   `{{.ns.a.b.c}}` token is a dotted-path lookup into a named namespace map
   (`spec`, `event`, `param`). There are **no operators, conditionals, loops,
   function calls, or evaluation** — a token like `{{.a + .b}}` or
   `{{len(.x)}}` does not match the token grammar and passes through as
   literal text, never evaluated (unit-tested). This is field reference, not
   a language. **Type-preserving:** a string that is *exactly* one token
   takes the resolved value's native JSON type (the structured binding §2.3
   implies); an embedded token renders with `fmt.Sprint`. Unknown
   namespace/field is an error (fail-closed). The Intent compiler's private
   `{{.spec.X}}` substituter is deleted and migrated onto this — one
   implementation, one review surface.
2. **Event → params, both Trigger launch paths.** Run-target: the engine
   resolves `t.Params`/`t.ViewParams` against `{event: payload}` at launch.
   Workflow-target: the payload rides in `DAGInput.Event`; each Step resolves
   its own `{{.event.x}}` bindings via a `ResolveStepParams` activity
   (substitution is I/O-free but not workflow-deterministic).
3. **Parametrized Views bind only at launch.** A View selector may carry
   `{{.param.x}}` in Labels values and Facet `Equals`; `ResolveSelector`
   substitutes a *copy* of the selector before `selectorSQL`, which still
   sees fully-resolved structured data (no SQL change; the selector-is-data
   property holds). The stored selector is unchanged and versioned — the
   binding is per-reference, not a version bump. `RunInput.ViewParams` (from
   a Trigger's `viewParams`, itself `{{.event.x}}`-templated) supplies the
   values. Two clean stages: event→viewParams, then param→selector.
4. **Validation timing (the one departure, recorded).** Non-templated params
   validate against the Actuator Contract at plan time exactly as before. A
   templated param **skips** the plan-time type check (the placeholder is not
   the value the schema must accept — and the event does not exist at plan
   time) and is **re-validated after substitution at launch**
   (`contract.ResolveActuatorParams`, on both the Run and Workflow-step
   paths). The Actuator never receives unvalidated params; only *when* a
   template-dependent field's violation surfaces moves from plan to launch —
   a resolved-data error, surfaced visibly (§1.8), not a static declaration
   error.
5. **Blast-radius containment (§4.3).** Parametrized Views are **rejected as
   Assignment/Baseline compile targets** (the compiler's `validateRefs`):
   the max-delta gate reasons about membership deltas from "Syncer relabel,
   View edit," never a param varying the resolved set — a gap the charter
   never closes. Confining parametrized Views to launch keeps them out of the
   compile/membership-delta path entirely.
6. **Fail-closed, no poison loops.** A binding that references a missing field
   or resolves to a contract violation never launches (no partial/empty-target
   Run). On the Run path this is a **terminal data error**: logged and the
   event dropped — *not* redelivered, because the same payload will never bind
   (a poison message must not loop). Infrastructure failures still nak/redeliver
   (ADR-0018's at-least-once guarantee, unchanged).
7. **Namespace scope at declaration.** `{{.event.x}}` is rejected on schedule
   Triggers (no firing event); `spec`/`param` are rejected on Triggers
   (`checkTemplateNamespaces`). CEL stays boolean-match only (unchanged).

## Consequences

- Flow 3 is live: an event naming host `DC0_H0_VM4` → a parametrized View
  bound via `viewParams: {host: "{{.event.host}}"}` → a Run resolving to
  **exactly that one host**; a payload missing the field failed the launch
  visibly and dropped the event once (no loop, no dispatch).
- **Deferred:** parametrized Views as Assignment/Baseline targets (needs the
  §4.3 membership-delta/max-delta machinery extended to param variance —
  itself an unclosed charter gap); Step-level `viewParams` on Workflow steps
  (v1 binds Step *params* to the event, not Step Views); `outputs.instances[*]`
  Step-output → next-Step binding (§2.3's other sanctioned binding — a
  separate slice).
- The one substituter now backs the compiler, the trigger engine, and View
  resolution — any future binding site reuses the same audited "no expression
  language" guarantee.
- charter-guardian PASS (no violations); the no-expression-language boundary
  holds by construction. Two consistency flags fixed in-slice: namespace
  scoping is now enforced at **declaration** for View selectors (param only)
  and Workflow-step params (event only), not just Triggers — a stray
  `{{.event.x}}` in a View or `{{.spec.x}}` in a Step fails its file, not at
  launch; and a parametrized View's plan entry is marked `paramDependent`
  with `memberCount` omitted, instead of running a bogus count against the
  literal `{{.param.x}}` selector (which printed a misleading 0).
