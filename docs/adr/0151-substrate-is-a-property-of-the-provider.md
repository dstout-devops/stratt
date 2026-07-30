# ADR 0151 — Substrate is a property of the PROVIDER, selected by composition; no declaration above it ever names one

- **Status:** **Proposed** (2026-07-30, steward) — **design only**. Supersedes the unwritten ADR-0151
  that `plugins/kubecompute` currently cites: kubecompute is not a special case, it is the first
  provider to declare `substrate: kubernetes`. **`charter-guardian` is OWED** on D2 before Accepted
  (a selector that resolves a provider by a declared property sits next to the §2.4 implicit-
  precedence line, and next to ADR-0118 D1's env-conditional-value refusal). No new Named Kind, no
  migration; one new field on a provider declaration and one alternative form of a binding entry.
- **Date:** 2026-07-30
- **Deciders:** steward
- **Charter sections:** §1.5 (sovereign contracts, multiple transports — the class is ours, the
  substrate is a transport-shaped detail beneath it), §1.1 (type the seams — substrate attaches to
  the provider boundary, never to an Intent), §2.4 (no implicit precedence — a binding resolves to
  exactly one provider or fails, never a ranked list), §1.4 (boring spine — this is one field and
  one selector, not a new resolution engine), §1.8 (an unresolvable substrate must name what it
  looked for and what it found)
- **Reconciles with:** **ADR-0118 D1** (`environments` is a MEMBERSHIP FILTER, not a value
  selector — the rule this ADR must not break, and the reason substrate does not live on the
  Environment), **ADR-0142 D4** (the deliberately thin Environment, which carries no coordinate for
  exactly that reason), **ADR-0110** (the `provisioning` class reach-path — `requires:` names a
  CLASS, never a provider), **ADR-0112 D6** (>1 provider for one class is disambiguated by a
  DECLARED binding, never guessed), **ADR-0147 D3** (a declared placement that cannot resolve
  withholds the launch spec — the guard that exposed the problem this ADR fixes), **ADR-0105/0106**
  (capability providers are provider-agnostic; a class is never one vendor), and
  **`plugins/kubecompute`** (commit `d2b6c57`), whose binding is written in the per-kind form this
  ADR replaces.

## Context

The capstone tried to build a host and the reconcile refused it, correctly:

```
placement target Intent/Subnet/app-subnet is built, but it carries none of the identity
schemes the resolved provider declares (it carries [aws.subnetId]; the provider declares
[kube.host])
```

`app-subnet` was built by OpenTofu against floci; the host was to be built by kubecompute. The
diagnosis at the time was "a host cannot be placed across substrates" — true, but it is the
SYMPTOM. The defect is that the estate had no way to say "this environment is Kubernetes" once, so
the topology was silently half-AWS and half-Kubernetes, and the first thing to notice was a
placement resolver comparing identity schemes.

The response that followed was worse than the defect: a new `Intent/Compute` named **`kube-app`**,
declaring no placement so that it would build. That is the anti-pattern this ADR exists to forbid.
An application-tier declaration had acquired a substrate in its NAME and a substrate-shaped hole
where its topology used to be. Deleted, unshipped, and recorded here because the reasoning that
produced it is the reasoning to guard against: it made the build pass by making the declaration
mean less.

**The steward's correction, which is the decision:**

> The substrate should likely be identified via compose and not strictly defined. At most, "vm",
> "aws", "kubernetes" in the blueprint or intent (and appropriate settings for those if needed).
> What we should NOT do is connect "aws" to "web-app". Ever. That's not how this works.
>
> Every single declaration should be variable enough that everything works together and is composed
> as needed. Ideally we eventually get to a state where I can simply change one line from "aws" to
> "kube" etc and the declared intent will migrate.

Two things already in the corpus decide most of the shape:

1. **`environments` is a membership filter, not a value selector** (ADR-0118 D1), and the
   Environment is deliberately thin for that reason (ADR-0142 D4) — it carries no region coordinate
   because a value an Intent INHERITED would make one Intent document mean different things in
   different places. So `substrate:` may **not** be a field on the Environment that Intents inherit.
   That is the same mistake as `kube-app`, moved one level up and made systemic.
2. **The capability-binding is already the sanctioned composition point.** It selects WHICH PROVIDER
   serves a class, per environment. It changes nothing about what an Intent means. Migration by
   changing a binding is therefore already legal — it is simply N lines today, one per Intent kind,
   each naming a provider by name.

So the destination is one short step from what ships: keep the binding as the composition point, and
let it select by a property the providers declare instead of enumerating them.

## Decision

### D1 — A provider DECLARES its substrate; nothing above it does

A provisioning provider's declaration gains one field:

```yaml
name: kubecompute
provides: [provisioning]
substrate: kubernetes # ← a FACT about this provider, like `provides:`
provisions:
  Compute: kubecompute-build
```

`substrate` is a short, closed vocabulary — `aws`, `kubernetes`, `vsphere`, `vm` — and it is a
statement about the provider, in the same category as `provides:` and `identitySchemes:`. It is
descriptive, not a knob: a provider does not get to be configured into a different substrate.

**No Intent, Blueprint, Assignment or View may name a substrate, ever.** `Intent/Compute` says it
wants a host and `requires: [provisioning]`; `Intent/Application` says apache. Neither says how or
where. This is the rule the `kube-app` Intent broke and the rule that makes the rest work: a
declaration that names its substrate cannot migrate, because the name IS the coupling.

### D2 — A binding entry may select by SUBSTRATE instead of by provider name

```yaml
name: dev-substrate
environments: [dev]
entries:
  - capability: provisioning
    substrate: kubernetes # ← ONE line; covers every Intent kind this substrate can build
```

Resolution: among providers of the class that are verified in this environment, take those whose
declared `substrate` matches and that advertise a builder for the kind being built.

- **Exactly one** ⇒ resolved.
- **More than one** ⇒ REFUSED, naming every candidate. Two providers of one substrate serving one
  kind is a genuine ambiguity, and picking by any rule (declaration order, name, "most specific")
  would be the implicit precedence §2.4 forbids. The per-kind `provider:` form below is how an
  author breaks the tie — deliberately, in the declaration.
- **None** ⇒ REFUSED, naming the substrate asked for and the substrates actually available. "No
  provider" and "no provider OF THIS SUBSTRATE" are different problems and must read differently
  (§1.8).

`substrate:` and `provider:` are **mutually exclusive on one entry** — an entry that carried both
would be answering the same question twice, and the two answers can disagree. The per-kind
`provider:` form is unchanged and remains correct for a mixed estate that genuinely wants one kind
served by a specific provider; the substrate form is the whole-topology default.

**The one-line migration is then literal:** change `substrate: kubernetes` to `substrate: aws` and
every provisioning Intent in that environment builds on AWS instead — the network, the hosts, and
whatever else the class covers, together, because they were never individually wired.

### D3 — A coherent substrate is what makes placement resolve

The refusal that started this is not softened, it is made unreachable in the normal case. Placement
compares the placement target's identity schemes against the resolved provider's (ADR-0147 D3);
when one substrate builds the whole topology, both sides come from the same provider family and
agree by construction.

A MIXED substrate within one topology stays refused, and the diagnosis should name the real cause —
not "identity schemes do not match" but that the subnet was built on one substrate and the host is
bound to another. That is the §1.8 improvement this ADR owes: the message an operator got was true
and unactionable.

### D4 — `params` stay opaque and substrate-shaped, and that is not a contradiction

An `Intent/Compute` carrying `params: {instanceType: t3.micro, ami: …}` is not naming a substrate;
it is carrying an opaque payload the resolved provider's own Action Contract types (ADR-0110). The
distinction is real and worth stating because it looks like a loophole:

- **Legitimate:** the estate declares provider-typed params, and a provider that does not understand
  them says so out loud (kubecompute reports the params it ignored rather than dropping them).
- **The violation:** a declaration whose NAME, VIEW, or STRUCTURE assumes a substrate — `kube-app`,
  or an `Intent/Application` that only makes sense on one provider.

The honest consequence, recorded rather than smoothed over: a fleet whose `params` are AWS-shaped
does not become correct by flipping one binding line. A full migration needs the params to be
substrate-appropriate too, and D2's one-line change moves the BUILDER, not the parameters. Making
params themselves composable — per-substrate overlays under one Intent — is deliberately NOT decided
here; it needs its own review precisely because it is the shape ADR-0118 D1 refuses when done
carelessly.

## Consequences

**Good.**

- One line migrates a topology, which is the stated goal, and it is a line in the composition layer
  where a substrate choice belongs — not in a declaration that describes what the estate wants.
- The rule is enforceable by inspection: an Intent/Blueprint/Assignment/View mentioning `aws`,
  `kube`, `vsphere` in a name or a required field is a defect, and that is a lint, not a judgement.
- Adding a substrate is adding a provider that declares it. Nothing above changes, which is the
  §1.5 claim actually exercised rather than asserted.
- The estate stops accumulating per-kind binding entries whose only content is "the same substrate,
  again".

**Costs, stated plainly.**

- **A second selector on one seam.** `provider:` and `substrate:` both resolve a binding entry, and
  a reader must know which is in force. Mutual exclusion keeps them from disagreeing but does not
  make the surface smaller.
- **`substrate` is a new closed vocabulary** in a project whose vocabulary is frozen at v1.0 (§2).
  It is a provider-declaration field rather than a Named Kind, but it is still a token the estate
  will grow attached to, and `vocabulary-linter` is owed on the value set before Accepted.
- **D4's honesty gap is real.** "Change one line and it migrates" holds for the BUILDER and not yet
  for provider-shaped params. Anyone reading only the headline will over-expect.
- Nothing here fixes a topology already built on the old substrate: changing the binding changes
  what gets built NEXT, and migrating existing instances is a decommission/rebuild question this
  ADR does not touch.

**Explicitly NOT decided here.**

- Per-substrate parameter overlays under one Intent (D4) — needs its own review against ADR-0118 D1.
- Whether `substrate` should also select non-provisioning classes (`configmgmt`, `certissuer`).
  Probably yes, and deliberately unaddressed until one is demanded by something shipping (§1.1).
- Migration of built instances between substrates.

## Follow-ups

1. `charter-guardian` on D2; `vocabulary-linter` on the `substrate` value set (both gate Accepted).
2. Implement D1 + D2, then delete `estate/capability-bindings/provisioning-kube.yaml`'s per-kind
   form in favour of one substrate entry, and re-prove the capstone chain — build a host, converge
   apache on it, deliver a certificate to it, as ONE act.
3. D3's diagnosis: make a mixed-substrate placement refusal name the substrates, not the schemes.
4. A lint that fails any Intent/Blueprint/Assignment/View naming a substrate — the `kube-app` guard.
