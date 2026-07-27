# ADR 0142 — An environment is declared, not just referenced; and it is not a Cell, a Site, or a coordinate

- **Status:** **Proposed** (2026-07-27, steward). Charter review by hand (this session's rules bar the
  subagent); §1.8/§2.4/§9 answered inline, and **D4 is deferred precisely because it is the part that
  needs the subagent**. **No new dependency. No new Named Kind** — `environments` is an existing field
  on existing declarations; this ADR gives it a referent.
- **Date:** 2026-07-27
- **Deciders:** steward
- **Charter sections:** §1.8 (never hide diagnosis — a silent no-op is the failure mode this closes),
  §2.4 (no implicit precedence; `environments` is a membership filter, never a value selector),
  §9 (no ontology creep), §1.1 (type the seam, not the world)
- **Reconciles with:** ADR-0057 (environment-scoped reconciliation — the seam this completes),
  ADR-0044 (Cells — the Named Kind most easily confused with this one), ADR-0032 (Sites),
  ADR-0110/0113 (environment-scoped capability bindings), ADR-0118 D1 (`rejectEnvKeyedValues` — the
  rule that BOUNDS this ADR), ADR-0115/0059 (`region` / `availability-zone` as projected Entity kinds)

## Context

ADR-0057 gave declarations an `environments []string` membership filter and a daemon-level
`STRATT_ENVIRONMENT`. It works. But an environment is **referenced everywhere and declared nowhere**:

- `ScopeToEnvironment` ([desiredstate.go:3384](../../core/internal/desiredstate/desiredstate.go#L3384))
  filters Assignments, Triggers and Baselines by `types.InScope(x.Environments, env)` — a pure string
  membership test against **no registry of legal names**.
- The live estate references exactly three: `dev`, `prod`, `vsphere-dc`. Nothing declares any of them.

So **a typo is a permanent silent no-op**. Write `environments: [us-east-01]` and the declaration is
filtered out of _every_ environment, forever, with no error at load, no warning at reconcile, and no
Finding. The estate looks correct in Git and does nothing. That is the §1.8 shape this repo has closed
repeatedly elsewhere — an uncontracted Action is refused at the diff, a Step naming an undeclared
Actuator is refused at the diff, a playbook missing from the content root is refused at the diff. The
environment reference is the one that got missed, and it is the cheapest of them to check.

It is also the **fourth** instance this session of one shape: a seam designed correctly, and a
producer or consumer never wired (cf. `mgmt.address`'s specified-but-unbuilt writers, `Intent/DnsRecord`
with no provider, `dns.fqdn` with no consumer, `opentofu-subnet-build` advertised and never written).

**A second, independent problem: the word is overloaded four ways**, and the conflation is natural
enough that it came up unprompted while planning this work. `types.Cell` even carries a `Region` field,
which makes "is a region a Cell?" a reasonable question with a non-obvious answer.

### What is deliberately NOT in scope, and why

The obvious next step — let an Environment carry a region coordinate and a DNS zone that scoped
declarations **inherit** — is **not decided here**, because the charter already has a rule pointed
straight at it. `rejectEnvKeyedValues` ([desiredstate.go:2553](../../core/internal/desiredstate/desiredstate.go#L2553))
refuses `values: {prod: {...}}` inside one Assignment, and states the principle:

> environments is a boolean MEMBERSHIP filter, "never a source of env-conditional values". The
> compliant shape is one Assignment per environment, each carrying flat values.

Inheritance is that same forbidden shape with the indirection moved into another file: the identical
Intent document would mean different things depending on the active environment. Whether _facts about
the environment itself_ (its region, its DNS zone — the same category as `Cell.Region`) are
distinguishable from _env-conditional values for consumers_ is a genuine §2.4 judgement call, not
something to assume while implementing something else. It is D4 below, deferred with the tension stated.

## Decision

### D1 — Environments are declared, in `environments/`

A new CaC declaration kind: `estate/environments/<name>.yaml`, parsed like every other kind, carrying
`name` and an optional `description`. That is all it carries in this slice (see D4).

It is **not a Named Kind** (§2 is frozen). `environments` is already a field on Assignments, Triggers,
Baselines, Connectors and CapabilityBindings; this ADR gives that field a **referent**, exactly as
`estate/capability-bindings/` gave `requires: [provisioning]` a resolution without adding vocabulary.

### D2 — An `environments:` reference must name a declared Environment

At estate load, every `environments` entry on every declaration is checked against the declared set. An
unknown name is a **load error** naming the file, the declaration and the known set — not a warning, and
never a silent filter.

**Compatibility rule, stated rather than implicit:** the check applies only when the estate declares **at
least one** Environment. An estate that declares none has no closed set to check against, and enforcing
would break every self-contained demo estate (ADR-0116 D1) for no safety gain. Declaring one is the
opt-in to the closed set. This mirrors `estate/plugins.yaml`: locality is not authority, and the estate
opts in on purpose (ADR-0137 D3).

### D3 — The four-way distinction, written down so it stops being re-derived

| Concept                                      | What it is                                                                                                                                                                        | Cardinality                     | Declared where     |
| -------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------- | ------------------ |
| **Cell** (Named Kind, ADR-0044)              | A region-local, single-writer **control-plane shard** — its own Postgres/NATS/Temporal/OpenFGA/object store. Carries `Region`, which is the region **the control plane runs in**. | Many Cells = one logical estate | `graph.cell` / CaC |
| **Site** (Named Kind, ADR-0032)              | An **execution locus** — where a Run's work is dispatched from. A Cell contains Sites.                                                                                            | Many per Cell                   | `sites/`           |
| **Environment** (this ADR, not a Named Kind) | A **reconcile scope**: which slice of one estate repo a daemon applies.                                                                                                           | Many per estate                 | `environments/`    |
| **Provider coordinate** (`params.region`)    | The substrate's own name for a place — an AWS region string, a vSphere datacenter. Opaque to core (§1.5).                                                                         | Per declaration                 | flat in `params`   |

And separately, **`region` and `availability-zone` are projected Entity kinds** (ADR-0059/0115, already
shipped by vcenter) — **observed facts** about a substrate, not declared scopes. An Environment does not
define a region; a region Entity is discovered by a Syncer.

The trap worth naming: **a Cell is not the estate's region.** One control plane routinely manages many
substrate regions. Treating them as the same thing would mean standing up a new
Postgres/NATS/Temporal/OpenFGA to manage a new cloud region — false and expensive.

### D4 — DEFERRED: whether an Environment carries inheritable facts

Not decided. The candidates are a provider coordinate (region) and a DNS zone — the latter wanted by the
reach-coordinate work (ADDR-1), whose cheapest producer derives a host's name from the estate's own
naming plus a zone. Both are _facts about the environment_, structurally like `Cell.Region`. Both would
also make one Intent document resolve differently per environment, which is what ADR-0118 D1 forbids.

This needs the **charter-guardian** bar (§2.4), not an inline judgement. Until it is decided, the
compliant shape stands: **one declaration per environment, each carrying flat values.**

## Charter alignment

- **§1.8** — the whole point: a silent no-op becomes a load error naming the file and the known set.
- **§2.4** — upheld by _restraint_. D2 adds referential integrity only; D4 explicitly refuses to make
  `environments` a value selector without a proper review.
- **§9 / §2** — no new Named Kind, no new ontology. One document type whose only job is to say a name
  is legal.
- **§1.1** — the declaration is the minimum a consumer demands. `description` is metadata; nothing else
  is added before something needs it.

## Consequences

- **Positive:** a typo'd environment fails at the diff. The overloaded word has one written definition.
  The estate gains a place to enumerate what environments exist — which is itself the "define a region
  from code" surface, in the sense the charter permits.
- **Negative / trade-offs:** one more declaration kind to keep in step. The D2 compatibility rule is a
  conditional gate, which is slightly less clean than unconditional enforcement — stated openly above
  rather than discovered later.
- **Follow-ups:** D4 (inheritable facts) under charter-guardian · `ScopeToEnvironment` filters only
  Assignments/Triggers/Baselines while Connectors and CapabilityBindings carry `Environments` and are
  filtered at their own consumption points; that asymmetry is undocumented and its doc-comment is
  already stale (ADR-0113 F2) — worth one pass · surfacing declared environments on the API/UI so an
  operator can see the set.

## Alternatives considered

- **Validate against the environments merely _referenced_ elsewhere** — rejected: it makes a typo legal
  as soon as it appears twice, and gives the estate no place to state intent.
- **Make `environments` a Named Kind** — rejected: §2 is frozen, and this is a scope label on existing
  declarations, not a new estate concept.
- **Let the Environment carry region + zone now** — rejected _for this slice_ (D4): defensible, but it
  runs directly at ADR-0118 D1 and deserves the review bar rather than being smuggled in beneath a
  referential-integrity fix.
- **Reuse Cell** — rejected: conflates the control-plane shard with the reconcile scope; see D3.
