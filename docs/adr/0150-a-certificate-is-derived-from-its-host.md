# ADR 0150 — A certificate is derived from its host: per-Entity template resolution and entity-scoped remediation

- **Status:** **Proposed** (2026-07-30, steward) — **D1–D5 implemented and LIVE-PROVEN**; D6 is a
  scope statement. One decision was amended by implementing it (D5, `cert.presented` is singular —
  recorded in place rather than rewritten). Prior-art
  scan done by hand (this session's rules bar the subagent). **Two reviews are OWED before this can
  move to Accepted and are called out in D5/D6:** `vocabulary-linter` on the new facet namespace and
  the `entity` namespace token, and `charter-guardian` on the §2.4 resolution rule. No new
  dependency. One new Facet namespace, one new template namespace, no new Named Kind, no migration.
- **Date:** 2026-07-30
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams — the Intent Contract still types `commonName`; what
  changes is where its VALUE comes from, not whether it is typed), §1.2 (projections, never a second
  truth — a host's FQDN is already a projected Facet, and restating it in Git per certificate is a
  second copy of a fact the graph owns), §2.4 (no implicit precedence — a template resolves from
  exactly ONE namespace and a missing fact fails closed; there is no fallback chain), §1.8 (an
  unresolvable template must name the Entity and the missing Facet, not silently issue a certificate
  for the wrong subject), §2.5 / §1.6 (the View remains the authorization unit — scoping a
  remediation NARROWS to a member, never widens), §1.4 (no new machinery: ADR-0024's existing
  substituter gains a namespace)
- **Reconciles with:** **ADR-0024 D1/D3/D4** (the one substituter, its `spec`/`event`/`param`
  namespaces, field-reference-only grammar, and the direct precedent that a value knowable only at
  launch BINDS at launch), **ADR-0030 D3/D4** (`Intent/Certificate` GA — which made `commonName` a
  required literal string and put `renewBefore` into the Baseline via `{{.spec.renewBefore}}`),
  **ADR-0050** (the `cert-issuer` reconcile Actuator and its Syncer half), **ADR-0023** (the
  compiler: one Baseline per (Assignment, route) over a whole View, Findings per Entity — the
  asymmetry this ADR turns into the mechanism), **ADR-0028** (View-scoped execution authz — why the
  Step's View must stay static), **ADR-0060** (many owners per Facet namespace, keyed
  `(namespace, owner_ref)` — what makes D5's host-side observation legal beside the CLM Syncer's),
  **ADR-0148 D1** (one Blueprint per delivered thing — the shape this ADR extends from applications
  to certificates), **ADR-0106 D1** (the `certissuer` CLASS, never the provider name), and the
  **CERT-2 born-on-target flow** (commit `cd08666`), whose static `viewName: managed-web` binding is
  the debt this ADR pays.

## Context

`estate/workflows/cert-issue.yaml` works end to end and is live-proven: a key is born on the target,
only the CSR travels, the CA signs it, the certificate is delivered. It also carries a comment
admitting the thing this ADR exists to fix — its `gather` and `deliver` Steps name
`viewName: managed-web` **statically**, because nothing in the estate connects a certificate to the
machine that will present it.

The first diagnosis was that a `cert` Entity carries only `cert.serial`, so it cannot be an ansible
target, and that the missing piece was therefore a `cert → host` Relation. **That diagnosis was
wrong, and the steward's framing is the correction:**

> certs are almost never going to be defined as "dns name" — the blueprints should be very
> templatable, "hostname" or "fqdn", so we can apply blueprints to groups and not have to make an
> intent for every single cert. this subsequently allows us to have shared certificate templates.

The question is not "which host does this certificate belong to". It is **"why is there a
certificate declaration at all?"** A certificate is not an independently-authored thing that must
then be matched to a machine; it is a **property of a machine**, derivable from facts the graph
already holds. Ask it that way and the modelling problem dissolves — and the one-Intent-per-cert
tax, which is the real adoption blocker, goes with it.

What the estate is forced into today, from
[`contracts/intents/certificate.schema.json`](../../contracts/intents/certificate.schema.json):

```json
"commonName": { "type": "string" },
"required": ["issuer", "commonName", "renewBefore"]
```

`commonName` is a required **literal**. A fleet of 200 web servers is 200 `Intent/Certificate`
files that differ in one string, 200 Assignments, and 200 Baselines — each an independent thing to
review, promote and withdraw. The Intent layer exists precisely so that a declaration says WHAT and
a View says WHERE; a per-host literal collapses that distinction and makes the Intent layer a
per-object registry, which is a CMDB by another route (a permanent non-goal, §1).

Three facts about the machinery, established by reading it:

1. **Blueprint routes see only `{{.spec.*}}`.** `resolveRemediationParams` builds
   `template.Namespaces{"spec": spec}` and nothing else. There is no way for a route to reference
   the Entity it matched. This is the direct blocker on templatable certificate naming.
2. **Params are resolved at COMPILE time.** `resolveRemediationParams` runs in the compiler and its
   output is stored on the Baseline, which covers the whole View. One resolved value, shared by
   every member — so a per-host value is not merely unsupported, it has nowhere to live.
3. **A remediation is NOT scoped to the drifted Entity.** `findingLaunch` returns
   `Params: b.RemediationParams` and the launched Workflow's Steps converge their own declared
   Views. One drifted host therefore converges every host in the View. Today that is merely
   wasteful and idempotent; under a per-host certificate it would be actively wrong.

The asymmetry in (2) versus (3) is the whole design: **the Baseline is per-View, the Finding is
per-Entity.** An Entity exists at the Finding, and only there. That is where a per-host value can be
resolved, and it is exactly the shape ADR-0024 D3 already chose for parametrized Views — bind at
launch, because launch is the first moment the value is knowable.

## Decision

### D1 — A certificate is DERIVED from its host; one Intent covers a group

`commonName` becomes **templatable**, and the canonical declaration is one `Intent/Certificate` plus
one Assignment over a View of hosts:

```yaml
# estate/intents/web-tls.yaml — ONE declaration for the whole tier
name: web-tls
kind: Intent/Certificate
spec:
  issuer: stratt-dev # the CLM ROLE (ADR-0106 D1: a class, never a provider name)
  commonName: "{{.entity.dns.fqdn}}" # ← derived per host, not authored per host
  validity: 720h
  renewBefore: 360h
  exportable: false
```

This is the **shared certificate template**: the Intent declares the naming POLICY and the issuance
parameters; the View declares the population; each member gets its own certificate. Adding a host to
the View issues it a certificate. Nothing is authored per certificate, ever.

The literal form keeps working unchanged — a template is a string that happens to contain a token,
and a `commonName` with no token resolves to itself. A singleton certificate for a service name that
is not a host FQDN (`api.example.com` fronting a pool) is still one Intent with one literal, which
is correct: that certificate genuinely is one thing.

**Why this and not a `cert → host` Relation.** A Relation would record, after the fact, an
association the estate never declared — leaving the operator to explain why _this_ certificate is on
_that_ host. Under D1 the association is not recorded, it is **implied by construction**: the
certificate is named from the host it is issued for. Nothing can drift between them because there
are not two facts. A Relation is also strictly more machinery (a new edge kind, a writer, a
correlation rule, a garbage-collection story) for strictly less clarity.

### D2 — A new `entity` template namespace, resolved PER-ENTITY at remediation launch

ADR-0024's substituter gains one namespace. Grammar and rules are **unchanged** — this is a
namespace, not a language feature:

- `{{.entity.<facet-namespace>.<path>}}` — a Facet value on the matched Entity, e.g.
  `{{.entity.dns.fqdn}}`, `{{.entity.os.kernel.arch}}`.
- `{{.entity.id}}`, `{{.entity.kind}}`, `{{.entity.identity.<scheme>}}` — the Entity's own
  coordinates, which a Facet namespace may not shadow (refused, §2.4).

CORRECTED after review: this ADR first documented `{{.entity.name}}`, which does not exist —
`types.Entity` has no Name, an Entity is known by its identity keys — while `kind` shipped
undocumented. And `labels` was exposed and is now REMOVED: a label is a free-form View selector
rather than a provenance-stamped fact, so deriving a certificate subject from one is a far softer
claim than deriving it from a Facet with a registered write-owner. §1.1 — it comes back if
something shipping demands it.

Field reference only: no operators, conditionals, loops or function calls (ADR-0024 D1 stands, and
`{{.entity.dns.fqdn | lower}}` must not become a thing — the moment a naming policy needs a function
it belongs in a Normalizer, where it is testable and provenance-stamped).

**Resolved at remediation launch, from the Finding's Entity**, for the reason ADR-0024 D3 gives for
parametrized Views: it is the first moment the value exists. The compiler continues to resolve
`{{.spec.*}}` at compile time and now **defers** any param containing an `{{.entity.*}}` token,
storing it on the Baseline unresolved — the same two-stage shape ADR-0024 already runs for
`event → viewParams → selector`.

**Fail-closed, and loudly (§2.4, §1.8).** A host with no `dns.fqdn` does **not** fall back to its
Entity name, its hostname, or the Intent's literal. It fails, and the failure names the Entity, the
missing Facet, and the Baseline that wanted it. There is no fallback chain, because a fallback chain
is implicit precedence and the failure mode it buys is a **certificate issued for the wrong
subject** — an outcome no convenience is worth. This is the one place this ADR is deliberately less
forgiving than an operator might like.

### D3 — A remediation launched from a Finding is scoped to that Finding's Entity

`findingLaunch` gains the Finding's Entity, and the launched Run targets **that Entity only**.

Without this, D1 and D2 are incoherent: a Workflow Step that converges its whole View cannot act on
a per-Entity resolved value, because there is no single Entity for the value to be resolved from.
With it, `cert-issue`'s `gather` and `deliver` Steps stop being statically bound — they converge the
one host whose certificate drifted, named from that host.

**The View still gates authorization.** The Step's `viewName` stays declared and static, so the
`runner on view:X` check (ADR-0028) is decided at launch against a known View exactly as it is
today. The Entity scope **narrows** the target set to a member of that View and is refused if the
Entity is not a member. This is the reason D3 scopes the Run rather than templating `viewName`: a
templated View would move the authorization gate off the launch and behind a resolution step, which
is a §2.5 change this ADR explicitly declines to make.

This is also a general improvement independent of certificates: today a single drifted host
converges its entire tier, which is a blast-radius surprise (§5) that the per-Finding descent
already implies is not happening.

### D4 — Templated params keep their Contract, validated after resolution

Per ADR-0024 D4's recorded departure: a param containing a token skips the plan-time literal check
and is validated against the Contract **after** resolution, before dispatch. `commonName` stays
`required` in the Intent schema — the naming policy is always declared, only its value is derived —
and a resolved `commonName` that is empty or not a string fails the Contract at the seam, before any
CSR is generated.

### D5 — The renewal expectation moves to a HOST-side observation

Under D1 the Blueprint binds a View of **hosts**, so its `observe` expectation must be readable on a
host. Today it reads `cert.expiry.notAfter` on a `cert` Entity, which the CLM Syncer projects.

The delivery play therefore reports what the host **actually presents**, and the Baseline's
`notBefore` window (ADR-0030 D4) reads that. This is a strict improvement in truthfulness, not a
workaround: the CLM's record says a certificate was _issued_, while the host's says one is _in
place_. A certificate issued but never delivered is exactly the failure an operator needs to see,
and observing the CLM cannot see it (§1.8, and §1.2's one-authority-per-fact — the host is the
authority on what the host presents).

The CLM Syncer's `cert.identity` / `cert.expiry` on `cert` Entities are **unchanged and still
authoritative for the CLM's own inventory**. Two observers of related facts on different Entities is
not a second truth; and since ADR-0060 re-keyed ownership to `(namespace, owner_ref)`, a second
registered owner is legal by construction.

**AMENDED DURING IMPLEMENTATION — `cert.presented` is SINGULAR, not keyed by common name.** This
ADR proposed `cert.presented.<cn>.{notAfter,serial,issuer}` so a host presenting several
certificates would be representable. Implementing it showed the cost: a per-CN key makes the
Blueprint's `observe.path` per-Entity too (`{{.spec.commonName}}.notAfter`, itself derived from
`{{.entity.*}}`), and expectations are evaluated by the DRIFT EVALUATOR, not resolved at launch —
a different mechanism from the launch-time param binding D2 ships, and a second one invented in the
same change. The shipped shape is therefore
`cert.presented.{commonName,notAfter,notBefore,serial,issuer}`: ONE Stratt-managed certificate per
host, which is the same stance ADR-0148 D6 takes for applications, and an expectation path
(`notAfter`) that is a constant. Per-Entity EXPECTATION resolution is the honest name for what the
keyed form needs, and it is now a follow-up rather than a thing this ADR quietly assumed.

**`vocabulary-linter`: CLEARED** (2026-07-30). `cert.presented` reads as a complement to
`cert.expiry` rather than a collision — different Entities, different authorities, legal
multi-source observation under ADR-0060. The `entity` template namespace is a lowercase token
beside `spec`/`event`/`param` and does not overload the Named Kind `Entity`. `kube.host` and
`host` are consistent with the frozen kinds.

**PREVIOUSLY OWED, now cleared:** `cert.presented` is a new core-model identifier and a new Facet
schema, so per CLAUDE.md it requires **`vocabulary-linter`** (charter §2 is frozen). It IS demanded
by a shipping Contract (§1.1) — the `ansible-certificate` Actuator's `facetNamespaces` and the
`deliver` Step's `facetWriteScope` — and pinned in `contracts/facets/cert.presented.schema.json`.
The linter has not been run; this ADR does not merge as Accepted until it has.

### D6 — Scope: this is a Blueprint capability, not a certificate feature

`{{.entity.*}}` is deliberately general. Nothing in D2 or D3 is certificate-specific, and the same
mechanism serves any Blueprint that needs a per-host value — a per-host config path, a per-host
listen address, a per-host DNS record. Certificates are the first tenant because they are where the
absence bites hardest.

**OWED BEFORE ACCEPTED:** `charter-guardian` on D2's resolution rule. A namespace that reads Entity
Facts into a remediation's params is close to the §2.4 implicit-precedence hazard and close to the
§9 ontology-creep line, and the fail-closed rule is the thing that keeps it on the right side. That
judgement should not be self-certified.

## Consequences

**Good.**

- One declaration per certificate POLICY instead of one per certificate. A 200-host tier is one
  Intent, one Assignment, one Blueprint — reviewable, promotable and withdrawable as one thing.
- The Intent layer keeps its meaning: declarations say WHAT, Views say WHERE. Certificates stop
  being the exception that proves the estate is a per-object registry.
- `cert-issue`'s static `viewName: managed-web` binding — the CERT-2 debt — is paid, and paid by
  removing a concept rather than adding one.
- Renewal becomes per-host drift on a fact observed from the host, which is what an operator
  actually wants to know.
- Per-Entity templating and entity-scoped remediation both outlive certificates (D6), and the second
  one shrinks the blast radius of every remediation in the platform.

**Costs, stated plainly.**

- **Three seams move at once** — the compiler (deferred resolution), the launch path (entity scope),
  and the Facet surface (host-side observation). That is more than any one of the recent
  single-seam ADRs, and the ordering matters: D2 without D3 resolves a per-host value against no
  host, and D3 without D2 narrows a blast radius nobody complained about. They ship together or not
  at all.
- **A fail-closed naming policy will bite.** A host missing `dns.fqdn` gets no certificate and a
  loud Finding. That is the correct behaviour and it will still read as a regression the first time
  it happens on a host whose FQDN nobody had noticed was unprojected.
- **A new Facet namespace is a frozen-vocabulary act** (§2) and carries the review cost D5 records.
- The existing literal-`commonName` estate keeps working unchanged, so there is no migration — but
  there is now **more than one right way** to declare a certificate (templated for a group, literal
  for a singleton). That is a genuine judgement an author has to make, and the ADR should be the
  place they find the answer: template when the certificate is a property of the machine, literal
  when the certificate is a thing in its own right.

**Explicitly NOT decided here.**

- Wildcard and SAN policy. `subject_alt_name` is currently derived in the play as
  `DNS:{{ cert_common_name }}`; whether SANs become their own templated list is a separate question
  and this ADR does not answer it.
- Whether `{{.entity.*}}` should also be available to Trigger params or Workflow launch inputs. The
  substituter would allow it; nothing demands it yet, and §1.1 says a seam gets typed when a
  shipping Contract asks for it.
- What happens when a host's FQDN CHANGES. The certificate's subject would then be derived
  differently and the old certificate is suddenly wrong. Renewal-as-drift will reissue it, but
  revocation of the superseded certificate is not addressed and should be booked as a follow-up.

## Follow-ups

1. Run `vocabulary-linter` on `cert.presented` and on the `entity` namespace token; run
   `charter-guardian` on D2's fail-closed resolution rule (both are gates on Accepted — D5, D6).
2. Implement in the order D2 → D3 → D5, each with its own falsification, then convert
   `estate/workflows/cert-issue.yaml` off its static `managed-web` binding and re-prove the live
   flow — including the case D2 exists to refuse, a host with no `dns.fqdn`.
3. Book the FQDN-change / superseded-certificate revocation question named above.
4. Fold the result into the W5 capstone, where the certificate leg is the last act.
