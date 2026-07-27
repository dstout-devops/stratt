# ADR 0142 — An environment is declared, not just referenced; and it is not a Cell, a Site, or a coordinate

- **Status:** **Proposed** (2026-07-27, steward). Charter review by hand (this session's rules bar the
  subagent); §1.8/§2.4/§9 answered inline. **D4 was deferred as the part needing the subagent bar, and
  is now RESOLVED (2026-07-27) — on a §1.2 argument stronger than the §2.4 one it was deferred for: a
  reach coordinate must be observed or caused, never computed.** **No new dependency. No new Named Kind** — `environments` is an existing field
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

### The question this ADR deliberately did not answer first

The obvious next step — let an Environment carry a region coordinate and a DNS zone that scoped
declarations **inherit** — was held back, because the charter already has a rule pointed straight at
it. `rejectEnvKeyedValues` ([desiredstate.go:2553](../../core/internal/desiredstate/desiredstate.go#L2553))
refuses `values: {prod: {...}}` inside one Assignment, and states the principle:

> environments is a boolean MEMBERSHIP filter, "never a source of env-conditional values". The
> compliant shape is one Assignment per environment, each carrying flat values.

Inheritance is that same forbidden shape with the indirection moved into another file: the identical
Intent document would mean different things depending on the active environment.

**D4 below now answers it — No — and the deferral earned its keep.** Working the question properly
found that §2.4 was not even the decisive objection: the decisive one is §1.2, and it is stronger.
Had this ADR assumed an answer while implementing a referential-integrity fix, it would have shipped
a zone field on the Environment and a correctness hazard with it.

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

### D4 — RESOLVED (2026-07-27): **No.** An Environment carries no inheritable facts

Deferred when this ADR landed, on the expectation that the blocking question was §2.4 —
`rejectEnvKeyedValues`' "environments is a membership filter, not a value selector", which
inheritance satisfies only by moving the indirection into another file. Working it through found a
**stronger and different objection**, and it settles the question without needing the §2.4 argument
at all.

**The rule: a reach coordinate must be OBSERVED or CAUSED — never COMPUTED.**

The candidate use was a DNS zone on the Environment, so a host's reach name could be derived as
`<instance>.<zone>`. That derivation asserts three facts Stratt does not own and does not observe:

1. the machine's hostname was actually set to `<instance>`;
2. a DNS record for `<instance>.<zone>` actually exists;
3. it actually points at **this** machine.

§1.2 is explicit that external systems of record stay authoritative and the graph is a projection. A
computed coordinate is Stratt **inventing a fact about DNS**. And the failure is not cosmetic: if DNS
disagrees, Stratt connects to the **wrong host** — a correctness hazard, not an inelegance.

This is the same argument [ADR-0143](0143-the-observed-reach-coordinate.md) D1 already makes one level
down, when it refuses a **bare** hostname because "whether `web-01` resolves depends on search domains
we neither control nor observe." Deriving a name from a zone is that same guess with more syllables.
Two independent lines of reasoning reaching the same refusal is the tell that the refusal is right.

**The strongest counterexample resolves the same way.** Kubernetes service DNS
(`service.namespace.svc.cluster.local`) genuinely IS deterministic — but that determinism is the
**provider's** knowledge, not the estate's. A K8s Compute provider projects the name it _caused_,
which is the observed producer again. It needs no environment-level zone. So even the case that most
looks like it wants derivation does not want it.

**Consequences for ADDR-1:** its three producers reduce to **two** — observed/caused (ADR-0143), and
registered (`Intent/DnsRecord`: declare the name, a provider creates it, and it is then a fact Stratt
caused rather than assumed). The "derived" producer is struck.

**Consequences for the region coordinate:** it dissolves too. A flat `params.region` in each
environment-scoped Intent is already the compliant shape ADR-0118 D1 prescribes, and already what the
estate does. Repetitive, explicit, correct — and "define a region from code" is satisfied by the
composition this ADR completes: an `environments/` declaration, environment-scoped
capability-bindings, and flat params.

So an Environment stays `{name, description}`. The deferral removed work rather than adding it.

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
- **Follow-ups:** ~~D4 (inheritable facts)~~ — **resolved: no** · `ScopeToEnvironment` filters only
  Assignments/Triggers/Baselines while Connectors and CapabilityBindings carry `Environments` and are
  filtered at their own consumption points; that asymmetry is undocumented and its doc-comment is
  already stale (ADR-0113 F2) — worth one pass · surfacing declared environments on the API/UI so an
  operator can see the set.

## Alternatives considered

- **Validate against the environments merely _referenced_ elsewhere** — rejected: it makes a typo legal
  as soon as it appears twice, and gives the estate no place to state intent.
- **Make `environments` a Named Kind** — rejected: §2 is frozen, and this is a scope label on existing
  declarations, not a new estate concept.
- **Let the Environment carry region + zone** — rejected outright once D4 was worked through, and on
  better grounds than the §2.4 rule it was first deferred for: a derived reach coordinate asserts DNS
  facts Stratt neither owns nor observes, and gets the wrong host when DNS disagrees (§1.2).
- **Reuse Cell** — rejected: conflates the control-plane shard with the reconcile scope; see D3.
