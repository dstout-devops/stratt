# Multi-team ownership, publish/consume, and sovereign handover

**Question.** Many teams build automation for each other. A network team makes VLANs, an infrastructure
team makes availability zones, a hardware team registers HCI, application teams publish apps and
playbooks — and every one of them consumes configurations, playbooks, collections and Workflows built by
teams they have never met. How does that work, how is it **owned**, how is it **gated**, and how does it
survive a **lights-off handover of a whole datacentre** to a different operator?

**Status.** A study, not a decision. It ends with an ADR queue.

**Evidence base (2026-07-26).** Vendor documentation and design proposals read on that date, linked
inline — documentation readings, not verified behaviour. The Stratt column is read from the tree and
linked, and is verified.

**Why now.** [ADR-0134](../adr/0134-tool-content-lives-beside-the-estate.md) moved Ansible playbooks out of
Workflow declarations into per-project directories, and claimed an isolation property it does not actually
deliver (see §4). That claim is what prompted the reading, and the reading found the problem is general:
it is not about playbooks.

---

## 1. The scenarios, stated as questions the platform must answer

Each is traced end-to-end in §6.

| #   | Scenario                                                     | The question underneath it                                                       |
| --- | ------------------------------------------------------------ | -------------------------------------------------------------------------------- |
| S1  | Network team builds VLANs and subnets                        | How does a producer publish a capability without publishing its implementation?  |
| S2  | Infrastructure team defines availability zones               | How does a consumer bind to a **kind of thing** rather than to a named provider? |
| S3  | Hardware team registers HCI into the estate                  | Who owns projected facts, and who may take authority over them?                  |
| S4  | Application teams publish applications and playbooks         | How does **content** become a first-class published, owned, versioned surface?   |
| S5  | Teams consume other teams' playbooks, collections, Workflows | What is the gate on consumption, and where does the authority to consume live?   |
| S6  | A datacentre changes hands, lights-off                       | Can placement and authority transfer as **one fenced operation**?                |

---

## 2. What five systems actually do

### Backstage — ownership as a relation, to a group

Every catalog entity carries `spec.owner`, and values are **interpreted as groups by default**; the
guidance is explicit that owning by individual breaks the moment somebody changes team. Ownership is one
relation among several well-known ones, and the set is instructive because it keeps three different ideas
apart:

| Category          | Relations                                                |
| ----------------- | -------------------------------------------------------- |
| Ownership         | `ownedBy` / `ownerOf`                                    |
| Producer/consumer | `providesApi` / `consumesApi`, `dependsOn`               |
| Composition       | `partOf` / `hasPart`, `parentOf` / `childOf`, `memberOf` |

**Take:** ownership, dependency and composition are three different edges. Collapsing them — "the team
that owns it is the team that made it is the team that uses it" — is the modelling error that makes
reorganisations expensive.

### Crossplane — the contract is owned by the platform; the claim is owned by the consumer

The XRD "is solely in the ownership of the platform team"; application teams write Claims in their own
namespaces. The stated payoff is **"platform evolution without breaking consumers"** — the consumer binds
an API, never an implementation, so the Composition behind it can be replaced.

**Take:** the publish/consume boundary is a **contract**, and it is deliberately owned by a different party
than the thing that fulfils it. This is the single most transferable idea here, and Stratt already has its
shape (§3).

### Automation Hub — a namespace is the unit of ownership, and publishing is gated

Content publishes into a **namespace**; a **group** holds the permission to upload to that namespace.
Uploads land in a **staging** repository as "Needs review" until an administrator **certifies** them into
**published**, where consumers can see them.

**Take:** producing is gated, consuming is not. That asymmetry is what makes self-service safe: the review
happens once at publish, not on every consumption.

### AWX — four verbs on a Project, and they are all different

AWX 24.6.1 separates, on a Project alone: **use** (reference it from a job template), **update** (trigger
the SCM sync), **admin**, **read** — plus **execute** on a Job Template and **use** on Inventory and
Credential. Teams hold roles directly; parent roles inherit child capabilities.

**Take:** "may run this automation", "may use this content", "may target these hosts" and "may use this
secret" are **four independent grants**. Any model with fewer collapses distinctions operators rely on.

### Argo CD — the cautionary one

`AppProject` constrains `sourceRepos`, `destinations`, and resource allow/blocklists. But Argo CD's own
proposal document records the hole: whoever can modify an Application **"can effectively circumvent any
restrictions that should be imposed by the AppProject by choosing another value for the `.spec.project`
field"**, and the multi-tenancy model "is of limited use in a purely declarative setup when full tenant
autonomy is desired… enforced through the Argo CD API instead of Kubernetes."

The app-of-apps pattern fails at scale for four distinct reasons: a **single blast radius**, a **central
file edit for every addition**, **no phased promotion**, and **all-or-nothing sync policy**. The escape is
generators — a _rule_ that admits many tenant-owned units instead of a list somebody maintains.

**Take, and it is the most important one here:** if a tenant can edit the field that names their own
authority, the boundary is decorative. Authority must live where the tenant cannot write it **and** be
enforced at the data layer.

---

## 3. What Stratt already has

More than expected, and it changes the shape of the work from "build a tenancy model" to "close three
gaps".

| Pattern                            | Stratt today                                                                                                                        |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| Producer publishes a **contract**  | ✅ capabilities: `provides: [ipam]`, `requires: [statestore, ipam]`, with `estate/capability-bindings/` selecting among >1 provider |
| Contract is the stable surface     | ✅ pinned, hash-verified Facet and Contract schemas; drift is blocking (§1.5)                                                       |
| Consumer-side claim                | ✅ Intent → Blueprint → Assignment                                                                                                  |
| Publish-time gate                  | ✅ admission policy (ADR-0073/0076) and Workflow Gates                                                                              |
| Group identity                     | ✅ org → team, with SCIM group→team as a **CaC mapping** rather than auto-teaming (ADR-0035)                                        |
| Placement isolation + fenced move  | ✅ Cells with `home_epoch`, `rehoming_to`, sealed Sources (ADR-0044/0045)                                                           |
| Authority projected from a mapping | ✅ `TupleAuthorizer` already unions **CaC tuples** with **projected** tuples derived from SCIM membership                           |

That last row matters more than it looks: **the mechanism for deriving authorization tuples from a
projection already exists and already ships.** Anything this study proposes to derive can use it.

S1–S3 are therefore **already expressible**: `netbox-ipam` provides `ipam`, `opentofu-network` and
`vcenter` provide `provisioning`, and an HCI Syncer is another Source. A consumer requires a capability and
never learns who implements it — Crossplane's split, already built.

---

## 4. The three gaps

### Gap 1 — nothing has an owner

There is no `ownedBy` on a declaration or on a projected entity. Ownership today is **"whoever can merge
that path"**. Every system in §2 makes ownership an explicit relation to a group, precisely so that it is
queryable ("what does the network team own?"), survives reorganisation, and can drive a Finding
("this Source has no owner").

### Gap 2 — content is a directory, not a published surface

Playbooks, collections and Workflows are the one class of thing teams publish **to each other** that has no
contract. AWX gives a Project four verbs; we have none of them. Concretely, this is where ADR-0134 went
wrong: it claimed one-Actuator-per-project delivers isolation. It does at **runtime** — only the selected
project's tree mounts — and it does **not** at authoring time, because a tenant who can write a Workflow
chooses which Actuator a Step selects, and so chooses whose content runs under whose write-ceiling.

That is Argo's `.spec.project` circumvention with a different field name, and ADR-0134 should be amended to
stop claiming it.

### Gap 3 — authority has no Cell dimension (the sovereignty gap)

There are three orthogonal partitioning axes in the system already:

| Axis            | Answers          | Where it lives                     |
| --------------- | ---------------- | ---------------------------------- |
| **org / team**  | _who decides_    | authz tuples (ADR-0009/0028)       |
| **Cell**        | _where it lives_ | `graph.source.cell`, entity homing |
| **Environment** | _which ring_     | `environments:` on declarations    |

Authz objects are **flat names** (`view:web-hosts`), so a grant cannot express _"team X may run against V
**in the EU cell**"_. Three consequences, and the third is the one that breaks the product promise:

1. Grants cannot be reasoned about per-region.
2. A handover requires hand-editing every tuple touching entities homed in that Cell.
3. **The outgoing operator keeps authority over entities that just moved.**

---

## 5. A constraint that shapes any answer: INV-3

ADR-0079 INV-3 states that authorization evaluation traverses **zero** graph Relations, and it is enforced
structurally — `TestINV3_AuthzConsultsNoGraph` fails the build if the `authz` package imports `graph`.

So ownership **cannot** be modelled only as a graph relation (it could never gate), and modelling it only
as tuples makes it invisible to the estate (no Findings, no "what does this team own?" View). The answer is
therefore forced, and it is a good one:

> **One declared source of truth, projected twice** — into the graph as an `ownedBy` Relation for
> queryability and Findings, and into authz as tuples for enforcement. Never authored twice.

This is exactly what SCIM membership already does through `TupleAuthorizer.projected`. The pattern is
proven in-tree; ownership would be its second consumer.

---

## 6. The scenarios, traced

**S1 — network team builds VLANs.** The team owns the `netbox` Connector declaration and the `netbox-ipam`
capability provider. It publishes `ipam`. Consumers `require: [ipam]` and are bound to a provider by
`estate/capability-bindings/`. _Works today_, except nothing records that the network team owns it (Gap 1),
and nothing gates **who may consume `ipam`** — capability consumption is currently open to any declaration.

**S2 — infrastructure team defines availability zones.** Projected entities from a provisioning Syncer; a
consumer selects them through a View rather than by name. _Works today_ (Gap 1 aside). This is the scenario
the existing model handles best, because the consumer never names a producer at all.

**S3 — hardware team registers HCI.** A Source projects hardware facts. Notably `source:<n>` **already has
an authz object** with an `adopt` relation, so "who may take authority over these projected facts" is
partly modelled — the one place ownership already bites. Gap 1 remains: the Source has no owner.

**S4 — application teams publish apps and playbooks.** _Does not work._ A project is a directory; there is
no publish, no owner, no version, no consumer-visible surface, and no gate. This is Gap 2 entire.

**S5 — teams consume others' content.** _Does not work safely._ Consumption is bounded only by who can
merge, and a consumer can select any Actuator, hence any content, under any grant. Gap 2 + the
circumvention.

**S6 — lights-off datacentre handover.** Placement transfer is **half built and the hard half is done**:
`home_epoch` fencing, `rehoming_to`, and sealed Sources already make the move atomic and split-brain-safe.
What does not move is authority (Gap 3). A handover today is a data-plane operation with a manual,
error-prone authorization postscript — which is the opposite of lights-off.

---

## 7. The model this points at

Not a decision — the shape the ADRs would argue about.

**7.1 Ownership.** Every declaration and every Source resolves to exactly one owning **team**; projected
entities inherit their Source's owner (the OpenFGA `parent_of` propagation idea, and it means projections
never need per-entity ownership authoring). Declared once, projected twice (§5).

**7.2 Content becomes a published surface.** An Ansible project — and later an OpenTofu module set, a Helm
value set — is an owned, versioned unit that **publishes** and is **consumed by name**, with AWX's verbs
kept apart: `use` (reference it), `update` (resync it), `admin`, `read`. Publishing is gated (Hub's
staging→certify); consuming is not.

**7.3 The anti-circumvention rule**, which generalises Argo's failure:

> **A declaration may never widen the authority of the team that owns it.**

Concretely: a write ceiling (`facetNamespaces`), a capability grant, and a content binding must live in a
**platform-owned path**, not in the tenant's directory. This makes ADR-0134's one-Actuator-per-project
correct **only if** Actuator declarations are platform-owned — which is a constraint on the layout, not a
detail.

**7.4 Admission by rule, not by list.** The app-of-apps lesson: the platform declares the **rule** that
admits tenant-owned units, and adding a project does not edit a central file. Stratt's admission policy
(ADR-0073/0076) is the right primitive; today it validates declarations rather than admitting tenants.

**7.5 Cell-scoped authority, and handover as one fenced operation.** A grant may carry a Cell scope. A
handover then becomes: bump the fence, and the cell-scoped grant set swaps atomically with placement. The
outgoing operator's cell-scoped grants become void **at the epoch**, with no tuple editing and no window.
This is the only proposal here that has no precedent in §2 — none of the five systems does regional
authority transfer at all — so it is the piece to design most carefully and the one worth the most.

---

## 8. Open questions

1. **Is a team's ownership global or per-Cell?** If a team owns a project and the DC changes hands, does
   the team follow the content or the Cell? Both are defensible and they imply different handover
   semantics.
2. **Should capability _consumption_ be gated at all?** Today any declaration may `require` any capability.
   Gating it adds safety and could equally add ceremony that kills self-service.
3. **What is the versioning unit for content?** Hub versions collections; AWX versions nothing and pins by
   SCM revision. `AWX-001` (`ansible.project` + `scm_revision`) already sits in this space.
4. **Does ownership of a projected entity ever differ from its Source's owner?** Inheritance is clean;
   real estates may have exceptions, and an exception mechanism is where models rot.
5. **Where does an Org sit?** AWX orgs contain projects; Stratt has `org` in authz and `ansible.org` as a
   projection. Whether Stratt grows a first-class Org container is unresolved (the parity audit files it
   as 🟡 "no first-class Organization container").

---

## 9. The ADR queue this implies

In dependency order. Each needs its own argument; none should be folded into another.

| #   | Decides                                                                                     | Depends on |
| --- | ------------------------------------------------------------------------------------------- | ---------- |
| A   | **Ownership** — `ownedBy` to a team, declared once and projected into both graph and tuples | —          |
| B   | **Content as a published surface** — the project unit, its verbs, and the publish gate      | A          |
| C   | **Authority objects** — the two AWX verbs authz lacks, enforced at the API door             | A          |
| D   | **Cell-scoped grants + fenced handover** — the sovereignty operation                        | A, C       |

**Immediately, and independent of all four:** ~~[ADR-0134](../adr/0134-tool-content-lives-beside-the-estate.md)
should be amended to withdraw its isolation claim~~ — **done**; its D2 now states that the boundary holds at
run time, not at authoring time, and names (C) as its dependency.

**The unblocked work right now is implementing ADR-0134 itself**, which is Accepted-shaped but **design
only** — its Implementation section carries the file-by-file plan, the traps (§1.4 tool-blindness,
ADR-0117 D6, the deliberate tripwires), and the one judgement it leaves open (which projects the reference
estate's plays belong to). It does not depend on A–D: the layout is safe to build now, and the authority
work hardens a boundary that is repo-review-enforced in the meantime. Doing it first also gives A–D a real
worked example to argue against rather than a hypothetical.

---

## Sources

- [Backstage — system model](https://backstage.io/docs/features/software-catalog/system-model/) ·
  [well-known relations](https://backstage.io/docs/features/software-catalog/well-known-relations/)
- [Crossplane — claims](https://docs.crossplane.io/v1.20/concepts/claims/) ·
  [platform configuration for app teams](https://blog.crossplane.io/crossplane-v0-13-paves-the-way-for-v1-0-with-platform-configuration-support-to-create-a-universal-cloud-api-for-your-app-teams/)
- [Automation Hub — namespaces and permissions](https://docs.redhat.com/en/documentation/red_hat_ansible_automation_platform/2.6/administer-assembly_working_with_namespaces) ·
  [collection approval workflow](https://docs.redhat.com/en/documentation/red_hat_ansible_automation_platform/2.5/html/managing_automation_content/managing-collections-hub)
- [AWX 24.6.1 — RBAC](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/rbac.html) ·
  [AAP multi-tenancy at scale](https://www.ansiblepilot.com/articles/aap-2-6-multi-tenancy-organizations-teams-rbac-at-scale)
- [Argo CD — applications outside the argocd namespace (the circumvention)](https://argo-cd.readthedocs.io/en/stable/proposals/003-applications-outside-argocd-namespace/) ·
  [declarative setup: projects](https://argo-cd.readthedocs.io/en/stable/operator-manual/declarative-setup/#projects) ·
  [app-of-apps vs ApplicationSet at scale](https://www.opendesk-edu.org/en/blog/gitops-argocd-app-of-apps-applicationset)
- [OpenFGA — authorization concepts](https://openfga.dev/docs/authorization-concepts)
