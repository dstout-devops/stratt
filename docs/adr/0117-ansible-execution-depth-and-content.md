# ADR 0117 — Ansible execution depth + content: the run-knob Contract (v5) and collections as pinned, external-sourced content

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN**; charter-guardian
  **PASS after fixes** (V1: the v4/v5 "opt-in" ladder was contradicted by the resolver — versions collapse
  to one live contract and `Step` has no version pin, so v5 is now additive-only; V2: `check` has a live
  writer in the AWX materializer, so v5 _deprecates_ rather than deletes it and the writer is redirected to
  `Step.DryRun`; F1: the Control-gating claim was unreachable — `ChangeContext` carries no Step params — so
  it is downgraded to declared+audited with content-blind gating booked; F2: `content` moved OUT of the
  run-time Contract into the EE build declaration, since a field the runtime ignores is the very defect D2
  corrects). **Amended 2026-07-24 (still Proposed), from the implementation-state survey:** (i) the claim
  that `eeImage` was "already honored" was FALSE — it is read by nothing, so per-Step EE selection is a
  prerequisite, resolved content-blind by **D3a** (image on the Actuator declaration, not a param);
  (ii) **roles** were omitted from D3 and are now in scope alongside collections.
  **Amended again 2026-07-24 (still Proposed), from the inventory-depth survey:** **D5** added — (a) ADR-0084's
  `port` was declarable but read by nothing, so the closed-coordinate justification ("every field is pulled by
  a consumer") did not hold; it now crosses the port typed and renders as `ansible_port`; (b) inventory
  **groups are refused** (a View _is_ the group; no source of truth for subsets exists);
  (c) a run that actuates **zero** hosts now FAILS — `ansible-playbook` exits 0 when a play matches nothing,
  and the hub's fold reported it green.
  **charter-guardian on D5 (2026-07-24): CHANGES REQUIRED → resolved.** V1 — D5c was **inert**: `govern`
  discarded the terminal's `ok`, so the shim's failure never reached the fold; fixed by asymmetric trust
  (a red terminal is believed, a green one still is not), which also closed the same hole for every
  `emitFatal` path. V2 — actuation counted any host, including ansible's implicit localhost, and is now gated
  on the resolved set. F1/F5 — the message asserted an unobserved cause and named `params.limit` as
  narrowing-to-empty; **live-verified against the EE image** that limit-to-empty is rc=**1** (already loud)
  while a non-matching play `hosts:` pattern is the real rc=0 path, and the wording now branches on ansible's
  own signal. F3 — D5b **miscited ADR-0055 G3** as a prohibition when it is a permitted charter-safe gap;
  re-grounded on the absence of any source of truth for subsets (§1.2/§9). F4 — `address: local` with a
  `port` was accepted and rendered by nothing (the very defect D5a fixes) and is now rejected. F2 — the WARN
  level is honestly recorded as not yet operator-visible, with the plumbing booked as follow-up (g).
- **Date:** 2026-07-24
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.5 (sovereign contracts, pinned + hash-verified), §1.4
  (boring spine, pluggable everything), §1.8 (never hide diagnosis), §2.5 (brokered credentials), §3
  (Ansible is subprocess-only — GPLv3 boundary), §7.3 (supply chain)

## Context

The next demo rung installs an application that requires a **TLS certificate**, and the enterprise thesis
needs Stratt to be a _better_ AWX/AAP — not merely an equal one. Both run through Ansible. But "we need a
fully-featured Ansible plugin" turns out to be the wrong frame, and the prior-art scan (adr-scout) is
unambiguous about why: **Ansible is Stratt's most-decided domain.** Six ADRs, four pinned Contract
versions, two Facet schemas, a live machine-credential, and **seven estate Workflows already executing
real plays** — including a real SSH converge against a managed node ([ADR-0084](0084-managed-node-reachability-address-facet.md)).
[`docs/aap-2.7-parity.md`](../aap-2.7-parity.md) scores Automation Controller **🟢 code-complete core**.

Three seams are settled and are **not** reopened here:

- **[ADR-0051](0051-ee-job-speaks-the-port.md) owns the transport.** Ansible runs as an ephemeral K8s Job
  in the EE image; `stratt-ansible` execs `ansible-runner -json` and emits the sovereign port's typed
  shapes. A long-lived gRPC ansible executor was **explicitly rejected** there and would breach §3 +
  [ADR-0046](0046-stratt-as-substrate.md) invariant 13.
- **[ADR-0084](0084-managed-node-reachability-address-facet.md) owns reachability.** `mgmt.address` → a
  typed `Target.Address`; `buildInventory` is the single place a connection coordinate is rendered.
- **[ADR-0086](0086-adopt-per-object-in-place.md)–[0089](0089-awximport-to-awx-plugin.md) own AWX migration**
  (adopt + the standing cutover reconciler). No second migration mechanism.

**What is actually missing** is narrower and sharper than "a plugin," and it is what blocks the next demo:

1. **The execution Contract is thin.** `ansible.input.v4` carries exactly `play`, `scm`, `extraVars`,
   `eeImage`, `check`. There is no **`become`** — so a play cannot install a package as root — and no
   `limit`, `tags`, `vault`, `forks`, `serial`, or `diff`. These are not exotic; they are what every real
   operator run uses, and their absence is the honest gap behind the 🟢 parity score.
2. **`check` is a Contract field no runtime path reads — but one path still writes it.** The shim maps
   check-mode from the port's `DryRun` bit (`plugins/ansible/shim.go`:
   `if req.DryRun { args = append(args, "--cmdline", "--check --diff") }`), per ADR-0051 MF6, which
   superseded [ADR-0019](0019-baselines-findings-v1.md)'s original `params.check`; the field survived in
   v4 as vestige. **A Contract that advertises a knob the runtime ignores is a lying seam** (§1.5, §1.8) —
   but the AWX materializer (`plugins/awx/materialize/workflows.go`) _does_ write it for imported check
   templates, so it cannot simply be deleted (D2).
3. **There is no content story.** The EE image installs `ansible-core` + `ansible-runner` and **no
   collections** — so `community.crypto` is unavailable and the certificate demo cannot run at all.
   Content is the 🔴 **biggest AAP gap** (no `requirements.yml` resolution, no Galaxy/private-Hub sync, no
   `ansible-builder` factory).
4. **Content sources are external systems.** Galaxy, a private Automation Hub, and a customer's AAP are
   **operator-owned, outside our control** — the discipline logged as **PLG-1** in
   [`enterprise-readiness.md`](../enterprise-readiness.md), which names this ADR as its first application.

**In scope:** the execution-knob Contract, check-mode consolidation, and the content posture (where
collections are declared, resolved, and pinned).
**Out of scope (named, not solved here):** `/api/v2` route breadth and notification sinks (parity P2/P3),
EDA rulebook depth (P4), the `ansible-builder` EE **factory** (P5 — this ADR sets the requirement it must
satisfy), and a collection **registry** (a Tier-3 item in the parity tracker — a product-scope call, not a
charter non-goal; the plugin model substitutes).

## Decision

### D1 — Deepen the execution Contract to `ansible.input.v5`: typed run knobs, never a passthrough

`ansible.input.v5` adds the operator knobs a real run needs, each as a **typed, validated field**:

| Field               | Shape                                           | Renders to                                     |
| ------------------- | ----------------------------------------------- | ---------------------------------------------- |
| `become`            | `{enabled: bool, user?: string, method?: enum}` | `--become`, `--become-user`, `--become-method` |
| `limit`             | string                                          | `--limit`                                      |
| `tags` / `skipTags` | string[]                                        | `--tags` / `--skip-tags`                       |
| `forks`             | integer                                         | `--forks`                                      |
| `diff`              | bool                                            | `--diff`                                       |
| `verbosity`         | integer 0–4                                     | `-v`…`-vvvv`                                   |
| `timeout`           | integer (seconds)                               | runner timeout                                 |
| `vault`             | `{credentialRef: string}`                       | `--vault-password-file` (file-injected, D4)    |

> **Dropped during implementation: `serial`.** An earlier revision listed a `serial` (rolling-batch) knob.
> `serial` is an ansible **play-level keyword with no `ansible-playbook` CLI flag** — it can only be
> honored by rewriting the operator's play, which this ADR will not do. Shipping it would have been
> precisely the lying seam D2 exists to remove, so it is omitted rather than faked. Rolling execution
> stays a property of the play (or a future play-templating decision).

**These are typed fields, NOT a free-form `args`/`cmdline` string.** An arbitrary-flag passthrough would be
an untyped seam at exactly the boundary §1.1 says to type, and an injection surface into a subprocess we
spawn with brokered credentials. Typing them makes escalation **declared and auditable**: `become` is a
reviewable field in Git and a recorded value on the Run, not an opaque substring.

Two rendering rules follow from that argument and are binding on the shim:

- Flags are rendered **from the typed value**, never string-concatenated from operator-supplied text —
  including `serial`, whose value must be emitted as a validated scalar/pattern, or the injection surface
  returns through the back door.
- `vault.credentialRef` **must name a ref already granted to the Step** (`Step.credentialRefs`), so the
  existing `use`-grant check at dispatch remains the single authorization path (§2.5). It opens no second
  grant channel.

**On policy-gating — the honest status.** Typed `become` is declared and auditable **today**; it is _not_
yet Control-gateable, and this ADR does not claim otherwise. `types.ChangeContext` — deliberately "the
unifier that keeps the spine content-blind" — carries Actor/Targets/BlastRadius/Environment/ChangeClass/
RiskScore/Labels and **no Step params**, so no Control can see `params.become`. Teaching the PDP to read
inside an ansible field would be precisely the `if ansible{}` that ADR-0046/ADR-0051 Phase 5b closed.
~~Gating is booked as a **follow-up via a content-blind mechanism**: the plugin declares a typed signal (a
`changeClass`, or a typed token on the existing non-precedence-bearing channel) that the PDP gates on —
never a tool-specific field.~~ — **done as [ADR-0122](0122-change-context-is-typed-and-partly-derived.md)
D3.** The signal is declared, but on the **Actuator** rather than in the tool's input Contract:
`elevatedInputs: [become.enabled]`. Core walks that declared path in each Step's params and derives the
core-owned `stratt.change/privileged` change-context label, which a Control gates on — so core still never
learns the word `become`, exactly as this passage required. The Contract would be the better long-run home
and is not available: `ansible.input.v5` is pinned and hash-verified, so annotating it in place is blocking
drift, and minting v6 pulls in D2's deferred `check`/`eeImage` removal. The mapping moves there whenever a
v6 exists for its own reasons. `stratt.change/` is a reserved namespace a launcher may not assert into, or
the gate would be defeatable by asserting the fact away.

Core stays **content-blind** (§1.4, ADR-0046): it validates the Contract as data (ADR-0015) and passes it
through; only `stratt-ansible` inside the EE knows these map to `ansible-playbook` flags. No `if ansible{}`
enters core.

**Ladder discipline (§1.5) — stated against how the resolver actually works.** Contract versions are
sibling files whose `.vN` suffix is _stripped_ when the registry keys them
(`core/internal/contract/contract.go`: `name, version = name[:i], n`, then `byName[name] = c`), and a
`Step` carries **no contract-version field** (`types/workflow.go`). So exactly **one**
`actuators/ansible.input` is live at a time — the highest version — and every Step validates against it.
There is no "opt in by referencing v5."

v5 is therefore a **replacement**, not a parallel version, and this ADR treats it as one: **v5 is
additive-only.** Every v4 field is retained with its meaning, so no existing Step breaks on the day v5
lands. (This is why D2 _retains_ `check` rather than deleting it.) A field is only removed in a later
version once no writer emits it — and removal is a **breaking** change to be declared as such, never
described as opt-in.

### D2 — Consolidate check-mode on the port's `DryRun` bit; **deprecate** `check`, and redirect its one writer

Check-mode has exactly one mechanism — the sovereign port's `DryRun` bit, mapped by the shim to
`--check --diff` (`plugins/ansible/shim.go`, ADR-0051 MF6) and driven by Baselines (ADR-0019) and
`stratt plan`. This ADR formally records that ADR-0051 superseded ADR-0019's original `params.check`.

**`check` is unread but NOT unwritten — and that distinction is load-bearing.** No runtime path reads it
(the spine is asserted never to write it, `core/internal/orchestrate/baseline_test.go`), but the AWX
migration path — the _only_ migration mechanism (ADR-0086–0089) — **does** write it:
`plugins/awx/materialize/workflows.go` sets `params["check"] = true` when an imported template's
`JobType == "check"`. Today that field is the sole carrier of "this template was a check job" across
migration.

So v5 does **not** delete it:

- **v5 retains `check`, marked deprecated and documented as ignored** (the runtime honors `DryRun` only).
  Retention keeps v5 additive-only (D1), so nothing breaks the day it lands.
- **The AWX materializer is redirected**: `JobType == "check"` must set **`Step.DryRun = true`**, not
  `params.check`. Until that lands, deleting the field would silently convert an imported AWX check
  template into a **converging apply** — a blast-radius change smuggled in by a cleanup, exactly what §1.8
  exists to prevent.
- **Removal is booked for a later version**, permitted only once no writer emits it, and declared as the
  breaking change it is.

`diff` remains a _separate_ v5 field (D1) because diff-without-check is a legitimate apply-time request —
"converge, and show me what changed" — distinct from "don't converge at all."

### D3 — Content (collections **and roles**) is an EE **build-time** declaration, exactly pinned and verified against the resolved set; the image digest is the run's single truth

Content is declared where it is _resolved_ — in the **Execution Environment build**, **not** as a field on
the run-time `ansible.input` Contract. The declaration is a **real Galaxy `requirements.yml`**, the same
file `ansible-galaxy install -r` and `ansible-builder` consume — Stratt invents no content format (§1.1),
and inventing one would break the charter §3 `ansible-builder` compatibility commitment:

```yaml
# ee/content/<variant>.requirements.yml — plain Galaxy format, no Stratt dialect
collections:
  - { name: community.crypto, version: "2.22.3" }
roles:
  - { name: geerlingguy.certbot, version: "5.2.0" } # both sections handled identically
```

**Roles are in scope, and were an omission in the first revision of this ADR.** `requirements.yml` has two
sections and Stratt handled neither: no role installation existed anywhere in the runtime, so roles worked
only incidentally, when an SCM-cloned repo happened to contain `roles/` next to its play. Since a
certificate/app-install play is exactly the kind that reaches for a community role, roles get the same
treatment as collections: declared in the EE build, installed at build time, exactly pinned.
(`plugins/ansibleproject` already _projects_ `ansible.role` Entities read-only, but that is observation of
a content root, not execution — a different seam, and dormant.)

> **Correction (implementation, ADR still Proposed).** An earlier revision of this section asserted that
> **`roles_path` plumbing was part of the work** ("there is no `roles_path`, no `ANSIBLE_ROLES_PATH` …").
> Measured against the real EE image, **no such plumbing is needed and none was added**:
> `/usr/share/ansible/{collections,roles}` is already on ansible's default search path, and
> `ansible-runner` **prepends** its own `project/` dirs rather than replacing the defaults. The observed
> resolution order is
> `/runner/project/roles:/home/runner/.ansible/roles:/usr/share/ansible/roles:/etc/ansible/roles:/runner/project`.
> Build-time content therefore resolves with **no** `ANSIBLE_ROLES_PATH`, no `ANSIBLE_COLLECTIONS_PATH`,
> and no `ansible.cfg`.
>
> The same measurement falsified a second claim: that an **"inline `play` can never use a role at all"**.
> That is true only of a _repo-local_ role, because the shim writes a single `play.yml` into `project/`. A
> **build-time-installed** role resolves fine from an inline play — verified by running one through the
> shim. So the inline-play limitation is narrower than stated, and build-time installation removes it
> rather than working around it.

**Why content stays out of the run-time Contract.** A `content:` field on `ansible.input.v5` would be a
Contract field the runtime does not honor — the _identical_ defect D2 exists to correct, re-introduced two
decisions later. Worse, once the factory exists, a Step's declared pins and the digest-pinned image's
actual contents would be two assertions of one fact with no stated winner. **The EE image digest is the
single truth about what content a Run had** (§1.2 in spirit: one authority per fact). A Step selects
content by selecting its EE image.

> **Correction (post-merge, ADR still Proposed).** An earlier revision claimed `eeImage` "already exists
> and is already honored." **It does not.** The field exists in `ansible.input.v4` but is read by nothing:
> `executeJobPlugin` builds `actuators.JobSpec{Files, Command}` with **no `Image`**
> (`core/internal/orchestrate/orchestrate.go`), so the dispatcher always falls back to the global
> `STRATT_EE_IMAGE` (`core/internal/dispatch/dispatch.go`). Per-Step EE selection is therefore a
> **prerequisite** of D3, not a given — and it carries a real §1.4 tension, because honoring
> `params.eeImage` means the core reading _inside_ an ansible param, the `if ansible{}` this ADR
> otherwise refuses. **D3a resolves it below.**

#### D3a — Per-Step EE selection is content-blind: a declared Actuator carries its image, not a param

The core does **not** learn to read `params.eeImage`. Instead, an EE is selected the way every other
plugin image already is — **by declaration**: a Step selects an Actuator, and the Actuator declaration
(ADR-0103, the same runtime-registered shape `helm`/`vcenter` use) carries the image. Two Actuator
declarations — say `ansible` and `ansible-crypto` — differing only in their EE image give per-Step
content selection with **zero** ansible awareness in core. `opentofu-network` and `opentofu-s3` already
share one plugin pod under two declarations, so this is a shipped pattern, not a new one.

Two things had to be built for that to be true, and neither was a given:

- **`JobSpec.Image` was dropped on the EE-Job path.** An earlier revision said the image "lands on
  `JobSpec.Image` from the registration struct exactly as the MCP path already does". That was true **only
  of `executeMCP`**. `executeJobPlugin` built `JobSpec{Files, Command}` with no `Image`, so a declared
  image was carried all the way to `PluginActuator.Image` and then discarded, and every ansible Run got the
  global `STRATT_EE_IMAGE` regardless. One line, but without it per-Step EE selection was **inert** — the
  same shape of defect as D5c's inert first half.
- **The CaC declaration could not express the govern grant.** `actuatorGrant` built a Grant from the
  declaration with **no** `FacetNamespaces` and **no** `IdentitySchemes`, while the boot-registered
  `ansible` inlines nine facet namespaces plus `host.name`. A declared ansible variant would therefore have
  run and then had **every fact write-back refused** — a strictly weaker Actuator that looks identical, the
  §1.8 failure mode this ADR keeps finding. Actuators now declare both fields, exactly as Connectors
  already do (ADR-0103's CaC grant), and `facetNamespaces` without `identitySchemes` is **rejected**: a
  facet grant with no scheme to correlate write-back by is honored by nothing, the same half-declaration
  rule as D5a's port-without-address.

`params.eeImage` is therefore **deprecated in v5 alongside `check`** (D2): retained so nothing breaks,
documented as ignored, removal booked once no writer emits it. This keeps the rule uniform — _a Contract
field the runtime ignores is a lying seam, and the fix is to delete the lie, not to teach the core a
tool's vocabulary._

**The default posture is therefore that the EE image is the content boundary**: content is installed at
build time with exactly-pinned versions, and the Run consumes that immutable, digest-pinned image. Three
integrity properties are enforced, and it matters which is which:

1. **Exact pinning, asserted positively, before the network is touched.** A version must match an exact
   `x.y.z` (or, for a role from git, a full 40-character commit id); everything else is rejected, including
   the unpinned `- ns.name` shorthand. The rule is an **allowlist on purpose**: the first implementation
   denied a list of bad tokens, and `devel`, `HEAD`, `dev`, `stable-2.19`, `2.22` and `1.0` all sailed
   through it as "exactly pinned" while naming a moving ref or an incomplete version. That mattered most for
   **roles**, which install from git tarballs where a branch name is a legal `version:`.
2. **Verify-don't-trust on the resolved set — for collections.** After install, installed versions are
   asserted against the declaration and a mismatch fails the build; `ansible-galaxy` can satisfy a request
   with a different version via dependency resolution or a redirected name. **This check is strong for
   collections and weak for roles**, and the asymmetry is stated rather than glossed: a collection's version
   is read from `MANIFEST.json`, which ships _inside_ the artifact, so a mismatch is detectable — whereas
   `ansible-galaxy` writes a role's `meta/.galaxy_install_info` from the **request**, so for roles the check
   compares the declaration against itself and cannot fail. The real protection for roles is rule 1.
3. **The image is the content boundary, and each Run states what it had.** Every Run emits its EE's content
   by name and version on its own event stream (`kind=ee-content`, transitive dependencies marked as such),
   because an image reference no operator can read answers no question during descent (§1.8).

> **Scope of the integrity claim — narrowed twice, and worth reading before citing it.** An earlier
> revision said content is "**hash-verified**"; the first narrowing said `ansible-galaxy` "verifies each
> artifact's checksum against the Galaxy API on download". Both overstated it.
>
> - **Collections:** the Galaxy API supplies a checksum and `ansible-galaxy` verifies the download against
>   it. The **registry is the authority**, so a _republished_ version at the same version number would not
>   be detected.
> - **Roles:** there is **no checksum step at all** — a role installs from a bare git tarball
>   (`https://github.com/…/archive/5.2.0.tar.gz`, observed). Only rule 1's pin constrains it.
>
> So an in-repo lockfile of per-artifact SHA-256 is not a nice-to-have; it is **load-bearing for the roles
> half**, and it was booked as follow-up (i) rather than implied by a word. **(i) has since shipped**, so the
> claim now reads: content is version-pinned by the declaration and **byte-pinned by
> `ee/content/<name>.requirements.lock.json`**, which the EE build verifies against the installed tree — the
> registry is no longer the only checksum authority for either half. Its own limits are stated with the
> follow-up: the digest is over the installed tree rather than the tarball, and the first lock is
> trust-on-first-use.
>
> Likewise, this ADR says the image digest is the single truth about a Run's content. The **mechanism**
> shipped selects by an image reference, which may be a mutable tag (`stratt-ee-crypto:dev` in dev).
> Digest-pinning production image references is the operator's job under §7.3, not something this decision
> enforces; the `kind=ee-content` event is what makes the actual content legible either way.

Run-time `ansible-galaxy install` is **opt-in and explicitly non-default**; when enabled it requires
a declared source, a brokered credential, and pinned versions — and, because it makes
the image digest no longer the whole truth, the shim **must verify the installed set against the declared
pins and fail the Run on mismatch** (§1.8 — no silent content drift).

Rationale — three charter forces converge on build-time:

- **§7.3 supply chain.** A run pod that fetches unpinned content from the network at execution time has no
  provenance story, no SBOM, and no reproducibility. Baked + digest-pinned does.
- **PLG-1 / §1.2 (external systems).** Galaxy or a private Automation Hub is an **external, operator-owned
  system** that may be unreachable, rate-limited, credential-gated, or simply down. **A Run must not fail
  because someone else's registry is having a bad day.** Build-time resolution moves that dependency out of
  the critical path of every converge.
- **Air-gap.** Enterprises run disconnected. A build-time boundary makes air-gap a _seeding_ problem
  (solvable) instead of a _runtime_ problem (not).

This closes the **trust + pinning** half of the 🔴 Automation Hub gap honestly, and states plainly what it
does not close: we ship no collection **registry** (a Tier-3 item in [`aap-2.7-parity.md`](../aap-2.7-parity.md)
— a revisable product-scope call, not a charter §1 non-goal), and **air-gap content seeding remains
an open follow-up**.

### D4 — External-first by default: content sources and managed nodes are systems we do not own (PLG-1)

This ADR is PLG-1's first application, and it resolves it for Ansible:

- **Content sources** (Galaxy, private Automation Hub, a customer's AAP) are external: **assume
  unreachable**, require an explicit declared endpoint, authenticate via a brokered `CredentialRef` (§2.5 —
  never ambient credentials, never material in the graph), and handle rate limits, pagination, TLS, and
  independent version skew as normal conditions rather than errors.
- **Vault passwords and machine credentials** extend the **existing** injector policy shape
  ([ADR-0009](0009-identity-authz-credential-brokering.md)/[ADR-0052](0052-secretbroker-port.md)) —
  `injection: [{key, as: file, name}]` — never a parallel credential path. `vault.credentialRef` (D1)
  resolves to a file at pod spawn, exactly as `web-machine`'s SSH key already does.
- **Managed-node reachability gets no new mechanism.** Bastioned/segmented fleets are served by
  **Site-local EE Jobs** ([ADR-0032](0032-sites-remote-execution-loci.md) + ADR-0051 Phase 6): the Job runs at
  the Site and forwards typed frames hub-ward. `mgmt.address` (ADR-0084) stays the only inventory seam.
  Dev's conveniences — a reachable in-cluster sshd pod, a floci instance on our own docker network — are a
  **harness, never the contract**.

### D5 — Inventory depth is the _existing_ coordinate finished, not a new grouping model; and a run that actuates nothing is a **failure**

"Inventories" was one of the five nouns this work was scoped against. Surveying it produced one thing to
finish, one thing to **refuse**, and one hole to close.

**D5a — Finish the `mgmt.address` coordinate: render `port`.** ADR-0084 defined the Facet as the closed
pair `{address, port?}` and justified the closure on the grounds that **every field is pulled by a
consumer**. That was not true: `addressOf` unmarshalled only `address`, so `port` was declarable in the
schema and reachable by no Run — a Contract advertising a knob nothing honors, the same defect D2 corrects
for `check`. `port` now crosses the plugin port as its **own typed `int32` field** (`ApplyTarget.port`,
field 5 — additive, `buf breaking`-clean), and the ansible shim renders `ansible_port` from it, exactly as
it renders `ansible_host` from `address`. It is deliberately **not** fused into `address` as `"host:port"`:
a fused string is an untyped seam the plugin must re-parse, which §1.1 forbids at a plugin boundary. `0` ⇒
undeclared, and the tool's own default applies — the core never invents `22`. The declared-estate Syncer
(the Facet's write-owner) gains the matching `port:` field; a port declared without an address is
**rejected**, not dropped, because half a coordinate reaches nothing.

**D5b — No inventory groups. The View _is_ the group.** AWX-parity instinct says "add groups to the
rendered inventory." Rejected — but **not** on the authority of [ADR-0055](0055-estate-composition.md) **G3**,
which an earlier draft of this decision miscited. G3 is a row in that ADR's _gap_ table marked "charter-safe
(guard OR-creep)", and ADR-0055 decision 3 says explicitly that _"charter-safe gaps (G5, G3) may proceed as
ordinary typed extensions with a short ADR each"_ — a green light, not a prohibition. The refusal stands on
its own merits instead:

- **There is no source of truth to render from.** `types.View` has no notion of a named subset and
  `ViewSelector` is a single AND-conjunction (`types/view.go`) — a View resolves to exactly one flat set. A
  group renderer would therefore have to **invent** its grouping inside the ansible plugin, from labels it
  chose by convention. That is a second truth for estate structure, living neither in Git nor the graph
  (§1.2), and a grouping ontology the core never modelled (§9 / ADR-0055 guardrail 1).
- **A View already _is_ the group** — that is ADR-0055's framing of the same primitive, and
  [ADR-0025](0025-awx-importer-and-ansible-scm-content-ref.md) already made the call in this direction by
  downgrading imported AWX groups to `awx.group.name` **labels**. Labels are the selector primitive, so the
  shipped answer to "target a subgroup" is **another View**, with `params.limit` (D1) for run-time narrowing.

Anyone who still wants inventory groups must first give **Views** a sub-grouping model — an ADR-0055
extension in its own right (which G3 permits), not an ansible-plugin feature. Doing it plugin-side would
prejudge that design and strand it behind one tool.

**D5c — A run that actuates no host FAILS — _and_ the spine must believe a plugin that declares its own
failure.** `ansible-playbook` exits **0** when a play's `hosts:` pattern names nothing in the rendered
inventory. **Live-verified** against the EE image, not assumed: such a run emits
`playbook_on_no_hosts_matched` and `ansible-runner` exits `0`. The hub folds
`Succeeded = sawTerminal && !failed` where `failed` is set only by a per-target result — and zero hosts means
zero per-target results — so the Run reported **green having changed nothing**. The §1.8 failure mode in its
purest form.

The fix is in two places, because the shim's half alone was **inert**:

1. **Shim (content-expertise).** It counts hosts that produced a terminal per-host result **and are in the
   core-resolved set**, and on `rc=0` with a non-empty resolved set and **zero** actuation emits a terminal
   `ok=false`. Membership matters: a play using `hosts: localhost` (ansible's implicit localhost, absent from
   the rendered inventory) produces a result the hub rejects as a confused deputy (MF4), so counting it would
   let a run that touched nothing in the View still read green. The message branches on ansible's own signal
   rather than asserting a cause it did not observe — with `no_hosts_matched` the pattern demonstrably matched
   nothing; without it the play matched but produced no result (a play with no tasks), a different fix for the
   operator. This lives in the shim, not the spine: only the ansible plugin knows a play can no-op (§1.4).
2. **Spine (content-blind).** `pluginhost.govern` **discarded the terminal's `ok` entirely** — the comment
   read _"ev.GetOk() intentionally ignored"_ — so the shim's `ok=false` changed nothing. Trust is now
   **asymmetric**: a **green** terminal is still not believed (the per-target results must agree — the
   original guardian fix is intact), but a **red** one is, because a plugin declaring its own failure is the
   most reliable signal available. This was a far larger hole than the one D5c set out to close: it silently
   greened **every** plugin-declared failure that produced no per-target result — invalid params, an SCM
   refusal, a git-clone or runner-spawn failure, a playbook syntax error. The rule is content-blind and
   applies to every plugin, not just ansible.

Two things are deliberately **not** claimed. `params.limit` narrowing the host list to **empty** is _not_ an
rc=0 path — ansible raises "…leaves us with no hosts to target" and exits `1` (also live-verified), so it
already failed loudly; `limit` is named in the diagnosis only as a possibly-disjoint contributor, never as
the cause. And the check is **not** "every target produced a result": narrowing 3 targets to 1 with `limit`
is a requested narrowing. Only actuating _nothing_ is vacuous.

Ansible's `playbook_on_no_hosts_matched` is also raised to **WARN**, which is the correct level at the port
and keeps _partial_ vacuity (one play of several no-opping) distinguishable. Note honestly that this is
**not yet visible to an operator as a warning**: `TaskEvent.Level` is dropped at the dispatcher and
`types.RunEvent` has no level field, so the event surfaces by kind and message only. Carrying level through
RunEvent → API → UI is booked as follow-up (g).

## Charter alignment

- **§1.1 (type the seams).** The Contract _is_ the plugin boundary; D1 types it rather than opening an
  untyped flag passthrough. No new Facet schema is introduced without a consumer.
- **§1.5 (sovereign contracts, pinned + hash-verified).** v5 is a new pinned version; v4 stays valid (no
  silent break). D2 removes a field the runtime ignored — a Contract must not lie. D3 extends pinning +
  hash verification to _content_, not just schemas.
- **§1.4 (boring spine, pluggable everything).** Core gains no ansible awareness; the knobs are data the EE
  shim renders. Content-blindness (ADR-0046, achieved in ADR-0051 Phase 5b) is preserved.
- **§3 (GPLv3 boundary) — non-negotiable, and reinforced.** Ansible and `ansible-galaxy` remain
  **subprocesses inside the EE image**; nothing is Go-linked (the Go shim builds in an isolated stage and
  arrives as a static binary; `ansible-core`/`ansible-runner` install into a separate Python layer).
  Build-time collection installation is one more subprocess in that layer writing _content_ into the image,
  so D3 strengthens rather than strains the boundary. **A distinct question, noted not settled:** baking
  third-party collections makes an EE a **distribution** artifact (collection licenses travel in the
  image) — separate from the linking question, and relevant only if Stratt ever publishes pre-baked EEs.
- **§2.5 (brokered credentials).** D4 routes every new credential need (vault password, Hub/Galaxy auth)
  through the existing CredentialRef + SecretBroker shape.
- **§1.8 (never hide diagnosis).** D2 removes a misleading knob; typed `become` and content pins make what
  a Run actually did legible in the descent.
- **§2 (vocabulary) — flagged for review.** The frozen AWX→Stratt mapping is `job template → Step preset`,
  `inventory → View`, `survey → input Contract`, `job → Run`, `credential → CredentialRef`, `project (SCM)
→ content ref`. This ADR introduces **no core-model identifier**: `become`/`limit`/`tags`/`collections`
  are _tool-domain fields inside the plugin's own Contract_ (data), exactly as `play` already is —
  the same quarantine ADR-0025 sanctioned for vendor-shaped attributes. **`collections` warrants explicit
  vocabulary-linter attention**, since the frozen mapping maps AWX's _inventory-collection_ sense to
  **View**; the sense used here is the Ansible-content sense, and it must not leak into a core noun, API
  route, or DB column.

### D6 — A big integer in a module result must not void the whole event (found while implementing D3)

Not a designed decision so much as a defect this work uncovered, recorded because it changes what D3 and
D5c mean in practice and because it was found only by running the real thing.

`RunnerEvent.EventData` is `map[string]any`, and `encoding/json` decodes every JSON number inside an `any`
into **float64**. Real module results carry integers far outside float64's range — an RSA-2048 modulus from
`community.crypto.openssl_privatekey` is **617 digits** — so `encoding/json` rejected the **entire event
line**, and `parseEvent` returned "not an event". The line then fell through to the MF5 diagnostic channel,
where it looked exactly like a runner banner. Everything typed about that event was lost: the per-host
`ItemResult`, the facts write-back, the drift fragment. Measured against the EE image, a five-task
certificate play emitted **1 `ItemResult` instead of 5**.

Two consequences, and the second is the reason this blocked D3:

- Before D5c this was **silently** wrong: zero per-target results still folded a green Run.
- After D5c it is **loudly** wrong. The vacuous-run guard counts actuation from exactly these events, so a
  one-task `openssl_privatekey` play produced `actuated=0` with `rc=0`, and the guard **failed a run that
  had genuinely done its work** — blaming the play's `hosts:` pattern, a cause it never observed. D5c
  converted a silent defect into a false failure, triggered by precisely the content D3 enables. Shipping
  D3 without this fix would have made the certificate demo report failure on success.

Decisions taken:

- **Decode with `UseNumber`.** Numbers stay `json.Number` literals, so nothing overflows and facts
  round-trip byte-exactly instead of through float64 (a big modulus was corrupted even when it _did_
  parse). Safe by inspection: the package asserts only `string`, `bool`, and `map` on decoded data — it has
  no `float64` assertion anywhere.
- **A misparse is now visible as a misparse.** The reason this survived is that an undecodable event was
  indistinguishable from ordinary output. A line that **is** an event but failed to decode now surfaces at
  **WARN** under `kind=unparsed-event`, separate from the non-JSON banners and tracebacks MF5 legitimately
  forwards.
- **An undecodable event outranks every other vacuous-run cause.** When the shim could not decode events it
  does not know whether hosts were actuated, so it still fails the Run — "I cannot tell what happened" is
  not a success — but says _that_, rather than asserting a cause it has no evidence for (§1.8).

The general lesson, and the third instance of it in this ADR's arc: **a plugin boundary that decodes an
external tool's output is a typed seam, and the types must come from what the tool actually emits, not from
what a reasonable schema would look like.** Nothing short of running `community.crypto` against a real
`ansible-runner` would have surfaced this.

## Consequences

- **Positive.** Unblocks the app-install-with-a-certificate demo (which needs `become` **and**
  `community.crypto`) and therefore the capstone chain. Closes the honest gap behind the 🟢 Controller
  parity score. Makes **privilege escalation a declared, reviewable, audited** value rather than an opaque
  flag string — with Control-gating a booked follow-up via a content-blind signal (D1), not a claim made
  today. Gives content a supply-chain story (pinned, hash-verified, digest-pinned EE)
  consistent with §7.3. Resolves PLG-1 for Ansible and sets the pattern for every later plugin. **D5c's spine
  half is the widest win here and was not the goal**: because `govern` discarded a red terminal, every
  plugin-declared failure with no per-target result folded SUCCEEDED — for every plugin, not just ansible.
  That is now closed platform-wide.
- **Negative / trade-offs.** A v5 Contract plus a build-time content path is real work, and baking content
  into EEs pushes **EE proliferation** — which makes the `ansible-builder` **factory (parity P5) more
  urgent, not less**; until it exists, EE builds stay hand-rolled. Build-time resolution trades run-time
  flexibility for reproducibility (deliberate); teams wanting ad-hoc collections must opt into the
  non-default run-time path or rebuild an EE. **Air-gap seeding remains unsolved.**
- **Follow-ups.** ~~(a00) Per-Step EE selection (D3a)~~ — **mechanism done**: `executeJobPlugin` now carries
  `JobSpec.Image`, and Actuators declare their own `facetNamespaces`/`identitySchemes` CaC grant (with
  `host.name` required when an EE-Job Actuator writes facets). `params.eeImage` stays deprecated and unread.
  The **estate declaration itself is deliberately not shipped yet** and lands with the certificate demo (f):
  an `ansible-crypto` Actuator that no Workflow selects would be "declared and read by nothing", and worse,
  see (l) — it would report _healthy_ while its image did not exist.
  ~~(a0) **Redirect the AWX materializer from `params["check"]` to `Step.DryRun`**~~ — **done** in the D2
  slice: `isCheckTemplate` maps an imported AWX check template onto the Step's DryRun bit, and nothing in
  tree writes or reads `params.check` any more. The field itself stays in `ansible.input.v5` as deprecated
  and unread — a pinned Contract is not edited in place (§1.5), so its **removal is a v6 change**.
  ~~(a) The `ansible-builder`-compatible EE factory (parity P5) — this ADR defines the
  contract it must satisfy. (b) Air-gap content seeding.~~ — **both done as
  [ADR-0124](0124-ee-content-supply-factory-and-offline-source.md)**, batched because they are one
  seam from two ends. (a) is an INPUT-FORMAT problem, not a build-engine one: `content.py` reads an
  `execution-environment.yml` and feeds its `dependencies.galaxy` to the pipeline that already
  byte-pins content — adopting `ansible-builder` itself would have traded away the lockfile, since it
  has no byte-pinning at all. Sections that do not carry over (`additional_build_steps`, custom base
  images, python/system deps) are **reported, never dropped** (§1.8). (b) is a SOURCE problem whose
  hard half was already solved: `install --artifacts <dir>` installs from local tarballs and reaches
  no registry, with the pin check and the lockfile check unchanged — so an air-gapped build gets the
  IDENTICAL byte guarantee a connected one gets rather than a weaker one. `task ee:content:pull`
  populates the directory on a connected machine and is deliberately not a relock. ~~(c) Survey → input-Contract enforcement on Steps
  (the named deferred item in ADR-0025/0026).~~ — **done, at the Workflow rather than the Step.** ADR-0118 D2
  built the seam this was waiting on, so an imported AWX survey now lands as `Workflow.inputs` and each
  answer binds into the Step's `extraVars` from `{{.launch.<var>}}` — the field this ADR's own v5 Contract
  already calls "the landing field for AWX launch extra_vars and survey answers". The detached
  `contracts/*.survey.schema.json` is no longer emitted (§1.2). Enforcing it surfaced a §2.5 defect that was
  latent while the document was read by nothing: a **password** question used to be typed as a string marked
  `x-stratt-sensitive`, which as a live launch input would have carried secret material onto the Run, into
  the audit stream, and out to `--extra-vars`. Password questions are now refused with a blocking
  re-broker entry, exactly as an imported credential is. It also forced a `/api/v2` fix — see ADR-0026,
  where the façade was discarding the Workflow it had just resolved. (d) `/api/v2` route breadth +
  notification sinks (P2/P3) — separate ADRs. **The notification half is done: ADR-0125**, which found the
  gap was not three missing drivers but three places where the spine named the one driver it had. `/api/v2`
  route breadth remains open. ~~(e) Update [`aap-2.7-parity.md`](../aap-2.7-parity.md) when v5 + content land, and
  **narrow or close PLG-1** in `enterprise-readiness.md`.~~ — **done.** Parity's Inventories, Execution
  Environments and Surveys rows now cite what actually shipped (D5a's `ansible_port`, D3/D3a's build-time
  content and per-Actuator EE, and `Workflow.inputs` for surveys). PLG-1 is **narrowed, not closed**: its
  design-rule half (D4) and its content half (D3) are settled, so the row is now solely about
  **reachability to an operator-owned fleet** — bastion/jump and Site-local execution are unproven, and
  every managed node we converge is still one we run. It stays Serious, because that is the half a
  customer meets first. ~~(f) A demo exercising the new knobs end to end.~~ — **done**: the
  [app-cert demo](../../demos/app-cert/README.md), **live-green on kind**. Treating it as the integration
  test for this work (per [ADR-0116](0116-demo-library.md)) paid for itself immediately — it found **four
  real defects in four runs**, each of them a §1.8 diagnosis failure that no unit test had reached:
  **(1)** an input Contract resolved by Actuator NAME, so a second declaration of the same plugin — the
  entire point of D3a — was rejected as uncontracted at parse time; the mechanism shipped and the thing it
  existed for could not be declared. Contracts belong to the TOOL, so resolution now falls back to
  `pluginIdentity` on every path that validates params (parse, API door, direct launch, template
  re-validation). **(2)** ansible's own failure events were forwarded at **INFO with no reason**: an
  unreachable host reached the descent as the bare word `runner_on_unreachable`, while the sentence that
  explained it sat in the event's result payload — and an EE pod's logs are deleted with its Job, so for
  anyone without shell on the target there was nothing at all. **(3)** the streaming governor kept the FACT
  of a red terminal and **discarded its message** — the same shape as the D5c defect one layer down, its
  message half — so a failed Run recorded no cause. **(4)** `mergeResults` folded `Succeeded` but **dropped
  `Error`**, so even a cause a slice HAD reported was lost before it reached the API. Defects 2–4 compound:
  a Run could fail with the reason known at three separate layers and reported at none.
  ~~(g) **Carry `TaskEvent.Level` through to the operator.**~~ — **done.** The port's typed level was
  dropped at the dispatcher and `types.RunEvent` had no field for it, so D5c's WARN on a no-op play and the
  shim's `unparsed-event` were correct at the plugin and invisible as warnings everywhere an operator looks.
  Now: `RunEvent.Level` carried from `TaskEvent.Level` (`LEVEL_UNSPECIFIED` maps to **empty, not `info`** —
  §1.8, an absent signal must not read as a benign one), a documented `RunEvent` schema in the OpenAPI spec,
  and the UI reading the stated level instead of guessing from the kind. Three things fell out of doing it
  properly. **One:** the SSE tail was the only response body on the API not built from the published
  schema — it marshalled the internal struct directly — so it now goes through a generated wire type like
  every other handler, and a drift test ties the spec to the struct field-by-field (the schema is pruned
  from Go codegen as unreferenced, so nothing else would have caught a divergence). **Two:** the UI's
  `RunEvent` was a hand-copied mirror of the Go struct; it is now the generated contract type (ADR-0091 —
  the OpenAPI seam is the only seam). **Three:** a stated `info` cannot be flattened onto the palette, since
  the port's level has no `success` member — ok-vs-changed is a _kind_ distinction, and ansible reports both
  at INFO, so an explicit `info` caps escalation but keeps the kind's downward refinement. The spine's own
  events were given levels in the same pass (`diagnostic-output`, `governance-rejected`, the bundle refusal).
  ~~(h) **A live demo asserting the vacuous-run guard.**~~ — **done**: the app-cert demo launches a
  `vacuous-run-guard` Workflow whose play matches no host and asserts the Run **fails AND names why**, so
  the hand-verified rc=0 proof cannot rot. Asserting the _naming_ half is what exposed defects (3) and (4)
  above — asserting only the failure would have passed against a Run that said nothing.
  ~~(i) **Per-artifact content hashes (a lockfile).**~~ — **done.** The pin rule bounds which _version_ is
  requested; only a content hash bounds which _bytes_ arrive, and until now the registry was the sole
  checksum authority for both halves (worse for roles, which had **no** checksum step at all). Closed with
  an in-repo lockfile beside each declaration — `ee/content/<name>.requirements.lock.json`, generated by
  `task ee:content:lock` from a real install and verified by every EE build. `content.py` grows a
  deterministic per-artifact digest: a sorted `(relative path, executable bit, file SHA-256)` list, so the
  hash depends on content and layout and nothing else — not mtimes, not install order, not the prefix.
  Symlinks hash by target rather than being followed. Exactly two exclusions, each because the bytes are
  produced by the _install_ rather than shipped by the artifact: `meta/.galaxy_install_info` (which is the
  very file that made the roles resolved-set check tautological) and `__pycache__`/`*.pyc`. **That closes
  the roles asymmetry**: a role's digest now comes from its installed bytes, not from something
  `ansible-galaxy` restates back from the request.
  Verification runs in both directions, and the second is the one that is easy to omit: every _locked_
  artifact must be installed at the locked version and digest, and every _installed_ artifact must be
  locked — otherwise a new **transitive** dependency (which `ansible-galaxy` resolves without asking)
  enters the image with no recorded hash and the lockfile quietly stops describing the image it claims to
  pin. Relocking is deliberately a separate, reviewable act and never part of a build or of `task ci`: an
  automatic relock would launder precisely the event the lockfile exists to catch. `task ci` runs the
  weaker offline half (`verify` now also asserts every declared artifact _is_ locked at the declared
  version) and **says** it is weaker — verifying a hash requires installing the artifact, which requires
  the network, which a pin check must not need. The build is the strong gate.
  Two limits stated rather than papered over: the digest is over the **installed tree, not the downloaded
  tarball**, so it detects a republished or tampered artifact but verifies nothing _before_ extraction —
  the guarantee is that no image survives a mismatch; and the first lock is **trust-on-first-use**, which
  is why the file lives in git and why the value is in reviewing its **diff**.
  Measured, not assumed: two `--no-cache` EE builds of `community.crypto 2.22.3` produce the identical
  digest (`2f03c37e…`), a corrupted locked hash fails the build with the republished-artifact diagnosis, a
  missing lockfile fails it with the fix, and a base EE (no declared content) still builds — there is no
  content whose bytes could change.
  ~~(j) **Surface `kind=ee-content` in the UI's Run descent**~~ — **done, via the port change this entry
  said it needed: [ADR-0121](0121-task-event-scope.md).** `TaskEvent.scope` (`SCOPE_RUN` / `SCOPE_TASK`,
  unspecified ⇒ empty) is the generic marker; it rides `RunEvent.scope` through the OpenAPI seam, the shim
  stamps the `ee-content` event `SCOPE_RUN`, the spine stamps its own run-level events (`pod-start-*`,
  `governance-rejected`, the Bundle refusal), and the log header gains an "N about this Run" filter beside
  the warned/failed one. The interface plane still matches on nothing tool-shaped. ADR-0121 D2 records the
  one member that was refused: `SCOPE_TARGET` would have duplicated `RunEvent.target` and been able to
  disagree with it, which is the two-discriminator defect ADR-0120 D1 found between `Framework` and
  `launchKind`. The original reframing, kept because its reasoning is what produced the ADR:
  Landing (g) made the honest shape clear: the UI cannot pin `ee-content` as run metadata without
  hardcoding one plugin's kind vocabulary in the interface plane, which is exactly the `if ansible{}` §1.4
  forbids. What shipped instead is the content-blind half that carries the actual §1.8 value: the log
  header counts the Run's warned/failed events and filters to them, so a WARN is findable in an uncapped
  stream — previously one line in fifty thousand, tinted by kind guesswork. Pinning run-scoped metadata
  properly needs a **generic marker on the port** distinguishing "this event describes the Run" from "this
  event describes a task" (a `scope` on `TaskEvent`, spine-blind to which plugin sent it). That is a port
  change, so it belongs in its own ADR — booked here rather than smuggled into the UI.
  ~~(l) **An unpullable EE image hangs a Run instead of failing (§1.8, platform-wide).**~~ — **done**, and
  the original entry **overstated the magnitude**: it said "up to 24 hours", attributing `RunAcrossCells`'s
  `ForwardChildRun` ceiling to Step dispatch. Measured: `a.Execute` runs under `RunAgainstView`'s options —
  `StartToCloseTimeout` **10m**, 3 attempts — so the real hang was ~30 minutes, still diagnosed only as a
  Temporal timeout. The defect itself was real and **not specific to ansible or to content**:
  `dispatch.waitForPod` switched on nothing but `Pod.Status.Phase`, so a pod parked in `ImagePullBackOff`
  stayed `Pending` and the loop heartbeated happily while kubelet's plain-text reason sat in the pod the
  whole time. Now `waitForPod` reads container `waiting` reasons (init containers included) and the
  `PodScheduled` condition, publishes them to the Run's own event stream so the §1.8 descent shows them
  without cluster access, and **narrates everything while failing only on reasons it positively recognizes**
  — a reason we have never seen reaches the operator but cannot invent a failure. `ImagePullBackOff` is a
  _backoff, not a verdict_, so pull-class reasons fail only after a bounded grace (`PodStartGrace`, default
  2m ≈ 4 kubelet pull attempts); `InvalidImageName` fails at once; `Unschedulable` is narrated and never
  failed, because a cluster autoscaler legitimately takes minutes. **The residual is now closed at the
  declaration end.** An EE-Job Actuator is still marked healthy without an image check — `enableActuatorLocked`
  skips the dial for a `jobCommand` Actuator because there is nothing to dial, which is structural and not a
  bug to fix there — so a declaration naming a nonexistent image reports healthy and fails its first Run ~2m
  later with the reason. The cheap partial this entry booked has shipped as
  `TestEveryDeclaredActuatorImageIsBuiltByATask`, beside the (k) guards: every `image:` an Actuator declares,
  in the reference estate and in every demo, must be produced by a `docker build -t` somewhere in the
  Taskfile.
  The **second** half is the one that actually bites and was not in the original booking: for a demo, the
  image must be built by that demo's own `demo:<name>:run`, resolved transitively through `deps:` and `task:`
  steps. An image some unrelated task builds is not a working demo — it is a demo that passes on a machine
  where the maintainer happened to build it last week and fails on a fresh clone, two minutes into the run,
  with a dispatcher error. `demo:app-cert:run` already builds `dev:ee-crypto:build` for exactly this reason
  and says so in a comment citing this entry; the guard is what keeps that comment true. Both halves were
  falsified: a typo'd tag and a removed build step each fail with the offending Actuator, image and task
  named.
  Its limits are stated in the test rather than implied: an Actuator with **no** image falls back to the
  floor's `cfg.EEImage`, which is a chart value and not an estate declaration; and it verifies that a build
  **exists**, never that it has been **run** — checking the latter needs a docker daemon, which `task ci`
  must not. That is also why the `ansible-crypto` declaration still lands with the demo (f) rather than
  alone: the guard proves the tag is buildable, the demo proves the image works.
  ~~(m) **Execution liveness is bounded far below what a Step is allowed to take (§1.8, platform-wide).**~~ — **done.**
  Found while measuring (l). Two ceilings contradict the Job the dispatcher itself creates
  (`ActiveDeadlineSeconds` **6h**): `a.Execute` inherits `StartToCloseTimeout` **10m**, so a Step whose pod
  legitimately runs longer can never succeed — it is killed and retried 3× into a timeout; and
  `HeartbeatTimeout` is **1m** while the heartbeat is driven _per output line_, so a single task that is
  silent for over a minute (`apt install`, a long `command`) trips a heartbeat timeout even though the pod is
  perfectly healthy. Nothing in-repo caught either one because every play we run finishes in seconds — both
  were found by _reading_ the ceilings while correcting (l)'s magnitude, not by any test. Fixed by making the
  Job's deadline the single authority: `dispatch.MaxStepRuntime` is what goes on `ActiveDeadlineSeconds`, and
  `execActivityOptions` derives the activity ceiling from it (with margin for spawn, log drain, and cleanup),
  applied to the two activities that run a pod — `a.Execute` and `a.ExecuteAction` (Actions are where the
  genuinely long ones live: a tofu apply routinely outlives ten minutes). The bookkeeping activities keep the
  short default deliberately, since a long ceiling there converts a wedged control-plane call into a silent
  stall. Liveness is now a ticker for the whole execution (`keepAlive`, 20s) rather than a function of tool
  chattiness, mutex-serialized because `activity.RecordHeartbeat` is not documented as goroutine-safe.
  ~~(k) **Migrate the boot-registered `ansible`/`script` Actuators to CaC** (ADR-0103's remaining
  boot blocks).~~ — **done.** Unblocked by D3a: the declaration could not express `ansible`'s bounded MF3
  grant until `facetNamespaces`/`identitySchemes` existed on the Actuator Kind, which is the whole reason it
  stayed hardcoded. Both boot blocks are **deleted** (not kept as a fallback — two registration paths for one
  name collide at §2.4 and make "which grant is live?" unanswerable from Git), replaced by
  `estate/actuators/{ansible,script}.yaml`, reconciled onto every replica by the connectorregistry with no
  strattd restart. The declarations are behaviour-identical to the blocks they replace: `actuatorGrant`
  derives `Source{Kind,Name}` from the Actuator name, which for these two _is_ the plugin identity, and an
  Actuator with no `image` falls back to the floor's `cfg.EEImage`. Three undocumented env knobs per
  Actuator (`STRATT_{ANSIBLE,SCRIPT}_{SHIM,PLUGIN_ID,SOURCE_NAME}`) are removed with them — nothing in the
  repo set any of them, and their replacement is a reviewable file.
  What this change also revealed, and what the slice therefore had to add: the migration converts a
  **deleted declaration into a run-time failure a long way from the deletion**. Seven Steps and Triggers in
  the reference estate name `ansible`; without the file each fails at dispatch with an unknown Actuator and
  nothing in the build says a word. `TestEstateDeclaresTheActuatorsItNames` closes that across the reference
  estate _and_ every demo, with the residual boot-registered names (`mcp`, `cert-issuer`) held in an explicit
  map that doubles as ADR-0103's migration tracker — shrinking it is how the next migration is proven. A
  second test pins the grant itself, because a dropped namespace would not fail to parse, it would silently
  refuse fact write-back at run time (the D5c failure mode), and because `script`'s `dryRunnable: false` is a
  safety property: flipped, a dry-run Step against an arbitrary script would be _executed_ and reported as a
  preview.

## Alternatives considered

- **A free-form `args` / `cmdline` passthrough string** (fastest path to "fully featured") — rejected. It
  is an untyped seam precisely where §1.1 demands typing, an injection surface into a credentialed
  subprocess, and unpolicyable: no Control can gate `--become` inside an opaque string, and the audit
  stream cannot say what a Run was permitted to do. Typed knobs cost more and are the product.
- **Run-time `ansible-galaxy install` as the default** — rejected. It puts an **external, operator-owned**
  system (PLG-1) in the critical path of every converge, defeats §7.3 provenance/SBOM, breaks air-gap, and
  makes runs non-reproducible. Kept as an explicit opt-in for teams who accept those trade-offs.
- **A long-lived gRPC Ansible executor** (linking or hosting `ansible-core` for speed) — rejected, and
  already rejected by ADR-0051: it discards per-run K8s-Job isolation and would breach the §3 GPLv3
  boundary (ADR-0046 invariant 13).
- **Re-render inventory / introduce a second connection-coordinate model** to carry richer per-host
  options — rejected. ADR-0084's `mgmt.address` Facet is the single reachability seam; per-host variance
  belongs in the graph as typed Facets, not a second renderer.
- **Build a collection registry / Galaxy mirror** — rejected here as a Tier-3 parity-tracker item, not a
  charter non-goal, so it is revisable without a charter amendment (the plugin +
  pinned-contract model substitutes for content breadth); the part that genuinely matters to enterprises,
  **air-gap seeding**, is booked as a follow-up rather than silently folded in.
