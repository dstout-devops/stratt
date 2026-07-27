# ADR 0144 — The reach coordinate can be REGISTERED: `Intent/DnsRecord` gets a provider, and a declared name becomes a caused fact

- **Status:** **Proposed** (2026-07-27, steward) — implemented. Charter review by hand (this session's
  rules bar the subagent); §1.1/§1.2/§1.5/§2.1/§2.4/§2.5/§1.8 answered inline. **One new dependency**
  (`github.com/miekg/dns`, assessed in D2). **No new Named Kind** — `Intent/DnsRecord` has been a kind
  since ADR-0059; this ADR gives it the provider it never had.
- **Date:** 2026-07-27
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth — DNS stays the SoR for names; the
  record is CAUSED and then OBSERVED, never asserted), §2.1/§2.4 (per-source write ownership and the
  ONE declared authority — no implicit precedence), §2.5 (the TSIG key is a brokered CredentialRef,
  never inline material), §1.5 (sovereign contract, RFC 2136 as a transport beneath it), §1.1 (the
  seam stays `{address, port}`; no device ontology), §1.8 (an unregisterable machine fails visibly)
- **Reconciles with:** ADR-0084 (the `mgmt.address` Facet — this builds the last writer it specified),
  **ADR-0143** (the OBSERVED producer — **D4 below amends its D3**), ADR-0142 D4 (which struck the
  DERIVED producer and left this one standing), ADR-0059 (`Intent/DnsRecord` + the `dns-record` Entity
  kind + the `dns.record` namespace, all shipped inert), ADR-0110 (the `provisioning` capability class
  — **D6 fixes the straggler this ADR-0110 migration left behind**), ADR-0060 (multi-source Facet
  ownership + declared authority — this is its third user and its FIRST authority declaration),
  ADR-0041 (per-key label ownership), ADR-0126 (machine credential + jump — the other half of reach).
  **Closes the second of ADDR-1's two surviving producers** in
  [enterprise-readiness.md](../enterprise-readiness.md).

## Context

ADDR-1 named three producers of a machine's reach coordinate. ADR-0142 D4 struck the derived one;
ADR-0143 built the observed one. This is the last: **registered** — the estate declares a name, a
provider creates the record, and the coordinate is then a fact Stratt **caused** rather than one it
assumed.

`Intent/DnsRecord` has shipped since ADR-0059 as a kind, a schema, and reconcile support, with **no
provider and no estate declaration** — the same designed-seam-no-producer shape ADR-0142 and ADR-0143
both opened on. Two findings sharpened what that costs:

**1. The kind is not merely unused — it is UNDECLARABLE.** ADR-0110 replaced the provider-coupled
`builder:`/`buildWorkflow:` seam with `requires: [provisioning]`, and shipped `subnet.v2`, `vlan.v2`
and `dmz.v2` to say so. `dnsrecord` never got a v2. It still **requires** `builder` and
`buildWorkflow`, and closes with `additionalProperties: false`. So an `Intent/DnsRecord` written the
way every other singleton Intent in the estate is written fails contract validation, and one written
the old way decodes into a `SingletonSpec` with an empty `Requires` and resolves to no provider at
all. "No estate declaration" was not an author's oversight; the door was nailed shut. That is why the
straggler is fixed here (D6) rather than filed as tidy-up: without it there is nothing to declare.

**2. A plugin cannot learn a machine's address any way except the port's own coordinate field.**
`ApplyTarget` carries `{name, identity_keys, address, port, jump}` and nothing else —
`ApplyTarget.vars` is passed through by the core and populated by no core path today. There is no
`graph` namespace in the Step template substituter (`event`, `steps`, `launch` only), and an Action is
targetless by construction. So "point this record at that machine" has exactly one legible answer with
the shipped seams: **the target's `mgmt.address`, resolved by the core and handed across as
`ApplyTarget.address`.** That constraint is what makes D1 true rather than merely tidy.

## Decision

### D1 — The registered producer STANDS ON the observed one; it does not replace it

The framing in ADDR-1 lists the producers as alternatives. Building it shows they **compose**, and
naming the composition is the most useful thing this ADR does:

> Observation tells Stratt where a machine currently answers. Registration binds the **estate's own
> stable name** to that coordinate, and thereafter the estate's name is the coordinate everything
> else uses — surviving the rebuild that changes the substrate's.

The corollary is a limit, and it is stated rather than discovered later: **registration cannot conjure
reachability where none is observed.** A machine with no `mgmt.address` — no guest tools, mid-boot,
a substrate that names nothing — has no coordinate to bind a name to, and the fleet-registration
actuation resolves no target for it. That is the §1.8 outcome, not a gap: "not yet reachable" stays
visibly not-yet-reachable, instead of acquiring a name that resolves nowhere.

An `Intent/DnsRecord` whose data the estate **declares** (a service alias, a delegation) has no such
dependency — see D5, which is why both entry points exist.

### D2 — The transport is RFC 2136 dynamic update + AXFR; the contract is ours

`dns` is a Connector/Actuator like any other: the sovereign port is the contract, and RFC 2136 is a
**transport beneath it** (§1.5). 2136 is chosen because it is the one update mechanism that is not a
vendor's: BIND, Knot, PowerDNS, Infoblox and BlueCat all accept it, so one provider covers the
authoritative DNS an enterprise already runs. Reads are AXFR over the same library and the same TSIG
key — the zone is enumerated, never guessed at record by record.

- **Auth is TSIG, and the key is a brokered `CredentialRef`** (§2.5) — resolved at pod spawn, never
  inline in a declaration, never logged, never written to the graph.
- **Honest exclusions, stated because "nobody looked" and "we looked and said no" must not render the
  same:** Windows AD DNS accepts secure updates only over **GSS-TSIG**, which this provider does not
  speak — an AD zone is out of scope until someone builds it. Cloud APIs (Route 53, Azure DNS, Cloud
  DNS) are **not** 2136 and are not a shortfall of this design: they are sibling providers behind the
  same `provisioning` class and the same Intent, exactly as ADR-0110 D3 intends.

**Dependency:** `github.com/miekg/dns` (BSD-3-Clause) — the de-facto Go DNS library, vendored by
CoreDNS, external-dns and cert-manager, tagged continuously (v1.1.72 current), no transitive weight
beyond `golang.org/x`. It meets §1.4 (boring, huge community) and §1.7 (evergreen: it tracks Go
releases within days). Assessed by hand; the dependency-scout subagent is barred this session.

### D3 — What the Syncer projects, and onto WHICH Entity

The zone is the Source; AXFR is the enumeration. Each record projects by a rule with no judgement in
it — which matters, because the alternative is guessing which host a record "means":

| Record          | Entity identity (`dns.fqdn`) | `mgmt.address` written |
| --------------- | ---------------------------- | ---------------------- |
| `A` / `AAAA`    | the record's **own name**    | the record's own name  |
| `CNAME`         | the **canonical target**     | the record's own name  |
| everything else | — (no Entity, no facet)      | —                      |

The CNAME row is the load-bearing one. A CNAME states "this name is an alias for that canonical
name", so **the Entity is the canonical one and the record's name is an additional coordinate for
it** — which is precisely how the estate's stable name lands on the machine the substrate named
something else. `dns.fqdn` is already the shared identity scheme `declared` and `vcenter` both emit,
so correlation is the graph's ordinary one and no new scheme appears.

`MX`, `TXT`, `NS`, `SRV` and the rest carry data, not a host coordinate, and are **not projected at
all** in this slice. `dns.record` stays the owned-but-uncovered namespace ADR-0059 registered: §1.1
forbids a Facet schema no shipping Contract demands, and nothing consumes a zone read-model yet.
Projecting the whole zone as graph state would be building a second DNS, not observing the first.

### D4 — `dns` is the DECLARED AUTHORITY for `mgmt.address` — this AMENDS ADR-0143 D3

ADR-0143 D3 registered `vcenter` as a second `mgmt.address` owner alongside `declared` and declared
**no** authority, reasoning that the two write disjoint Entities in practice and so "there is nothing
to declare." Adding a writer that **deliberately overlaps** ends that. It is worth being exact about
what would otherwise happen, because the failure is silent and severe:

`FacetValuesByEntities` ([reader.go:203](../../core/internal/graph/reader.go#L203)) resolves one
effective value per Entity. One owner yields its value; many owners yield the **one declared
authority**; many owners with **none** declared yields **nothing** — the read is omitted, fail-safe,
and a contention Finding surfaces. Correct in general, and here it would mean that registering a name
for a vSphere VM makes that VM **unreachable**: two non-authoritative owners, no value, no target.
The feature would break the thing it exists to enable, and it would break it at dispatch, far from
the change.

So `plugins/dns/estate/connectors/dns.yaml` carries `authoritativeFacetNamespaces: [mgmt.address]`,
and the semantics are the ones we want anyway: **when the estate has registered a name for a machine,
that name is the machine's reach coordinate** — it outranks whatever the substrate incidentally calls
the box. This is ADR-0060's declared-authority mechanism used for the first time in the estate, it is
explicit and reviewable in Git, and the store enforces at-most-one (§2.4). The substrate's own
coordinate stays fully visible on `net.guest` / `instance.*` for diagnosis; it stops being the thing
Runs dial.

Where the DNS Syncer writes nothing, nothing changes: a single-owner read still returns its value.
The authority declaration only bites on contention, which is exactly the case it is for.

### D5 — Two entry points, one Actuator, one producer of the coordinate

A record whose data the estate **declares** and a record whose data is **the machine's observed
coordinate** are different jobs, and the shipped seams shape them differently — an actuation Step
requires a `viewName` (refused at load without one), and an Action is targetless.

1. **Singleton — `Intent/DnsRecord` → the gated `dns-record-build` Workflow → the targetless
   `dns/register` Action.** The estate declares name, type, and data (a service alias, a delegation,
   a record for something Stratt does not manage). Resolved through `requires: [provisioning]` and the
   `dns` provider's `provisions: {DnsRecord: dns-record-build}` (ADR-0110 D3), gated like every other
   build, projecting the record back as a `dns-record` Entity carrying the singleton correlation
   label.
2. **Fleet — the `dns` Actuator over a View.** "Every machine in this View has a name in this zone."
   The record's data is **not declared**: it is each target's `ApplyTarget.address`, which the core
   resolved from the graph. An address ⇒ an `A`; a name ⇒ a `CNAME` to it. That branch is a fact about
   the value in hand, not a policy, and it is why no IP appears in Git.

Neither path writes `mgmt.address`. **The Syncer is the sole producer** (D3): the record is caused,
then read back from the zone, so the graph never holds a name no server has confirmed. That is §1.2
held exactly — cause it, then observe it; never assert it. It also means the coordinate is
rebuildable: drop the graph, re-sync, and it returns from DNS rather than from a Run's memory of what
it once did.

**What this does NOT give, stated because the obvious next sentence would be wrong:** observing the
zone does not make the coordinate self-retracting. A Facet, once written by a source, persists until
that source overwrites it or the Entity is tombstoned — there is **no facet-retraction path anywhere
in `core/internal/graph`**, so a record deleted behind Stratt's back leaves its `mgmt.address` row
standing. That is a property of the graph write path, not of DNS (ADR-0143's observed coordinate
survives a VM losing its guest tools the same way), and it is booked as a follow-up rather than
papered over here. `dns/deregister` exists so the ordinary path — Stratt removes the record it made —
is at least a deliberate act with a Run behind it.

### D6 — `dnsrecord.v2`: the ADR-0110 straggler

`contracts/intents/dnsrecord.v2.schema.json` replaces `builder`/`buildWorkflow` with
`requires` (must contain `provisioning`), matching `subnet.v2` / `vlan.v2` / `dmz.v2` field for field.
v1 stays embedded and pinned — Contract versions are sibling documents (ADR-0015), never edits.

**The guard is the more valuable half.** Nothing checked that the singleton Intent kinds carry the
same declaration seam, so one kind silently kept a superseded one through an ADR that migrated the
others. A test now asserts that every kind `provision.SingletonSpec` decodes declares `requires` and
declares no `builder`/`buildWorkflow`, so the next kind added to that family cannot repeat it.

## Charter alignment

- **§1.2** — DNS remains the system of record for names. Stratt causes a record, then **observes it
  back**; the graph never asserts a name it has not read from the zone. The coordinate is rebuildable:
  drop the graph, re-sync, and it returns from DNS.
- **§1.1 / §9** — no schema change to `mgmt.address`; the seam stays the closed `{address, port}`.
  `dns.record` stays uncovered because no Contract demands it. No device ontology, no new Named Kind.
- **§1.5** — the port is the contract; 2136 is a transport. A cloud-API DNS provider is a sibling
  behind the same class and the same Intent.
- **§2.4** — exactly one declared authority, declared in Git, enforced at-most-one by the store. No
  priority field, no last-writer-wins.
- **§2.5** — the TSIG key is a CredentialRef; no material in any declaration or log.
- **§1.8** — a machine with no observed coordinate stays visibly unregistered rather than acquiring a
  name that resolves nowhere.

## Consequences

- **Positive:** ADDR-1 closes. A host can be declared with **no `address:` in Git** and still be
  reachable, which is the specific thing ADDR-1 called wrong. The estate's names survive a rebuild
  that changes the substrate's. ADR-0060's declared-authority mechanism gets its first real use.
- **Negative / trade-offs:**
  - Making `dns` authoritative for `mgmt.address` means a stale record **outranks** a fresh
    observation. That is the correct reading of "the estate registered this name" — but with no facet
    retraction (D5), a record deleted outside Stratt keeps outranking observation **indefinitely**,
    not until the next sync. Accepted with eyes open: the alternative (no authority) makes every
    registered machine unreachable the moment it is registered, which is strictly worse, and the
    failure here needs someone to delete a record behind the platform's back.
  - `dns.fqdn`-only correlation means a CNAME whose canonical target is not an Entity Stratt knows
    projects nothing. Deliberate: auto-vivifying a stub for every name in a zone is the writable-CMDB
    non-goal.
- **Follow-ups:** **facet retraction — the one this ADR surfaced and did not fix.** Nothing in
  `core/internal/graph` removes a Facet a source has stopped reporting: `UpsertFacet` is
  upsert-only, and `TombstoneAbsent` operates on Entities, so the only lever available is one that
  would delete a shared Entity (a `dns.fqdn` tombstone scheme here would tombstone the vCenter VM
  itself). That is why this plugin declares **no** tombstone scheme. Every Syncer in the repo is
  affected; it simply has not been load-bearing until an authoritative owner existed, and D4 is
  exactly what makes it so — a stale AUTHORITATIVE value does not merely linger, it outranks a live
  observation. Tracked as **FACET-1** in [enterprise-readiness.md](../enterprise-readiness.md); it
  needs its own ADR, because it is a §1.2 write-path decision and "absent from this window" must not
  become confusable with "the window failed" · a GSS-TSIG
  (AD DNS) provider · a cloud-API provider behind the same class, which is the honest proof that
  D2's transport choice is not load-bearing · `GetFacetOwner`
  ([registry.go:46](../../core/internal/graph/registry.go#L46)) still `QueryRow`s a multi-row table
  and returns an arbitrary owner — now with a declared authority in play it is worth closing rather
  than merely noting (carried from ADR-0143) · the fleet-registration Trigger cadence is a Workflow
  today; a Baseline over the zone would make it desired-state proper.

## Alternatives considered

- **Have the record's build Action write `mgmt.address` as a Run-provenance project-back.** ADR-0084's
  own wording invites it ("a build from its project-back"). Rejected: a build asserts what it
  _intended_, and the graph would keep claiming the name after someone deleted the record. Observing
  the zone costs one sync cycle and is true continuously.
- **Derive the record name from the machine name plus an Environment zone.** Struck already by
  ADR-0142 D4, for the reason that generalises D1: computed is neither observed nor caused.
- **Project the whole zone as graph state.** Rejected: §1.1 (no Contract demands a zone read-model)
  and §1.2 (that is a second DNS, not a projection of the first).
- **Declare `vcenter` authoritative instead, and let DNS be advisory.** Rejected: it inverts the
  point. The estate's registered name is the durable coordinate; the substrate's is the incidental one.
- **Skip the authority declaration and let contention raise a Finding.** Rejected on evidence: the
  fail-safe read omits the value, so the Finding would be accompanied by an unreachable machine — a
  diagnosis without a working system, when the answer is knowable and declarable up front.
