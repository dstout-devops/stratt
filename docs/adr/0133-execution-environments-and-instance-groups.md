# ADR 0133 — An AWX execution environment is a supply-chain fact; an instance group is a placement model we already have

- **Status:** **Accepted** (2026-07-26, steward) and **implemented** — EE projected with derived
  digest-pinning, Baseline shipped, instance groups declined; `task ci` green. Charter review by hand (this session's rules bar the
  subagent); §1.2/§2/§7.3/§9 answered inline. **No new dependency.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth), §2 (frozen vocabulary), §7.3
  (supply chain — pinned-digest images, never floating tags), §9 (no ontology creep)
- **Reconciles with:** ADR-0032 (Sites — the execution locus), ADR-0044/0045 (Cells), ADR-0117 D3/D3a
  (an EE is a **build artifact** and an **Actuator declaration**, never a run-time parameter), ADR-0124
  (the EE content factory), ADR-0128 D2 (traversal-not-scan), ADR-0130 D3 (the precedent for declining
  half a slice with an argument), ADR-0131 (the poll-cost budget)
- **Closes:** `AWX-007`. **Explicitly does NOT close `AWX-008`** — see D4.

## Context

The audit filed these together as "mapping questions nobody has asked", and asking finds they are not the
same question at all.

- An **execution environment** in AWX is a named pointer to a **container image** (plus a pull policy and,
  optionally, a registry credential). Job templates reference one.
- An **instance group** is a pool of AWX execution nodes — AWX's answer to _where does this run_.

Both have Stratt counterparts, and that is exactly what makes them look alike and behave differently. Our
EE is a build artifact whose content is pinned at build time (ADR-0117 D3, ADR-0124) and selected by an
**Actuator declaration** (D3a). Our placement is **Sites** (ADR-0032) and **Cells** (ADR-0044). Both
counterparts are desired state.

The tempting symmetry — "project both, they're objects in the estate" — is wrong for one of them.

## Decision

### D1 — Project the execution environment, and frame it as **supply chain**, not placement

`ansible.executionenvironment` (name, image, pull policy) as a ninth owned namespace, with
`ansible.template --runs-in--> ansible.executionenvironment`.

The reason this is worth a namespace when instance groups are not: **the object is a container image
reference**, and an image reference is a supply-chain fact Stratt has a strong, already-stated position on
(§7.3: pinned-digest images, never floating tags). "Which of my job templates run an image nobody pinned"
is a question the mirror can answer, that matters independently of any migration, and that **no other
system in the estate can answer** — AWX will happily run `:latest` forever and never mention it.

That framing also keeps it out of the trap D4 describes. We are not mirroring where AWX runs things; we are
mirroring **what image it runs them in**, which is a property of the artifact, not of the placement.

### D2 — `digestPinned` is derived, and that is the point of projecting at all

Beside the raw `image`, the projection carries a derived boolean: whether the reference is digest-pinned
(`…@sha256:…`) rather than tag-floating. Deriving it in the plugin is content expertise living in the
plugin (ADR-0089's rule), not reinterpretation: it is a structural property of a reference string, the same
kind of parse the content half already does when it classifies an inventory file's format.

The raw `image` is kept alongside, because a Finding that says "not pinned" without saying **what** is not
pinned fails §1.8.

### D3 — Ship the consumer: `awx-ee-digest-pinned`

A facet-observation Baseline over a new `awx-execution-environments` View asserting `digestPinned == true`.

This is the first governance surface in the AWX mirror that applies **our own** standard to **their**
estate rather than auditing AWX by its own lights — `awx-schedule-enabled` asks an AWX question, this one
asks a Stratt question about AWX. It is the same standard SEC-5 holds us to internally, which is the honest
way round: we do not get to flag a customer's floating tags while shipping our own.

### D4 — Instance groups are NOT projected, and the reason is the interesting half

AWX instance groups are **the answer to a question Stratt answers differently**. Projecting them would put
a second placement model in the graph beside Sites and Cells — one that looks authoritative, that nothing
routes on, and that no dispatch decision will ever consult. That is precisely the shape ADR-0130 D3
declined for role grants, and the objection is the same: a convincing model that does not govern is worse
than an absent one, because someone will eventually build on it.

There is a place for the concept, and it is **not the mirror**: an instance group is a **cutover mapping** —
"your `dmz-nodes` group becomes this Site" — which belongs to the adopt transform, where `inventories →
View` and `credentials → CredentialRef` already live. That is the audit's own `mapped` category, and it is
where this should land if it lands.

Booked as **AWX-019**, and stated honestly: it is `none` today, not `mapped`, because no transform handles
it yet. Calling it mapped before the code exists would be exactly the overclaim this ADR is trying to
avoid.

### D5 — Poll cost: one collection read, associations free

`/execution_environments/` is a **collection** (O(1) per poll), and the template's EE association rides
`summary_fields.execution_environment` on an object already being read — zero extra requests, the same
shape as labels in ADR-0132. The collection count moves 8 → 9 and the poll-cost test's literal moves with
it, deliberately, which is the mechanism ADR-0131 D1 exists to force.

## Charter alignment

- **§1.2.** Read-only projection; AAP stays the SoR for its own EE registry.
- **§2.** `ansible.executionenvironment` is the vendor's own noun under the `ansible.` prefix — the mirror
  of a foreign object, distinct from our own EE concept (a build artifact + an Actuator declaration,
  ADR-0117 D3a), exactly as `ansible.template` is distinct from Workflow.
- **§7.3.** The projection makes our stated image-pinning standard checkable across the mirrored estate.
- **§9.** One namespace with a shipped consumer; one deliberately declined (D4).

## Consequences

- **Positive.** An operator can ask "what is my AWX estate actually running, and is any of it pinned"
  across every job template at once — a question that has no answer inside AWX. The `runs-in` edge makes
  image blast radius a traversal, matching the credential and label patterns.
- **The Baseline will fire loudly on a real estate**, because most AWX EEs are tag-referenced. That is a
  true finding rather than noise, but it is a **large** one on first run, and unlike `awx-superuser-review`
  (a handful of accounts) it can be one Finding per template. Called out so it is a decision an operator
  makes knowingly; severity is `warning` and the cadence daily.
- **We are holding others to a standard we have not finished meeting.** SEC-5 is still red — our own charts
  default to floating tags. Shipping this Baseline while that is true is defensible only because it is
  written down in both places; if SEC-5 stays red indefinitely, this row becomes something to be
  embarrassed about rather than proud of.
- **`ansible.executionenvironment` is the longest namespace in the set** and reads awkwardly beside
  `ansible.org`. Accepted over `ansible.ee`, which is the vendor's shorthand and cryptic in a graph an
  operator browses.

## Alternatives considered

- **Project both, symmetrically.** The tempting move, and rejected in D4: instance groups would install a
  second placement model that never governs.
- **Project the EE as a field on the template** (`eeImage: "…"`). Cheaper, and rejected for the third time
  on ADR-0128 D2's rule: it answers by scan, cannot carry the pull policy, and gives the image no identity
  to hang the next supply-chain fact on (a digest, an SBOM reference, a scan result).
- **Project the EE but skip the Baseline**, leaving `digestPinned` for someone to query. Rejected: §1.1
  wants a demander, and more practically an unpinned-image fact that nothing surfaces is a fact nobody
  reads.
- **Map instance groups → Sites in the projection** (rather than at adopt). Rejected: a projected Site
  would be a Site the dispatcher does not know about — the second-truth failure in its most literal form.
