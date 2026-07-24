# ADR 0117 — Ansible execution depth + content: the run-knob Contract (v5) and collections as pinned, external-sourced content

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN**; charter-guardian
  **PASS after fixes** (V1: the v4/v5 "opt-in" ladder was contradicted by the resolver — versions collapse
  to one live contract and `Step` has no version pin, so v5 is now additive-only; V2: `check` has a live
  writer in the AWX materializer, so v5 *deprecates* rather than deletes it and the writer is redirected to
  `Step.DryRun`; F1: the Control-gating claim was unreachable — `ChangeContext` carries no Step params — so
  it is downgraded to declared+audited with content-blind gating booked; F2: `content` moved OUT of the
  run-time Contract into the EE build declaration, since a field the runtime ignores is the very defect D2
  corrects)
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
   but the AWX materializer (`plugins/awx/materialize/workflows.go`) *does* write it for imported check
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
| `serial`            | string \| integer                               | `--cmdline` serial (rolling)                   |
| `diff`              | bool                                            | `--diff`                                       |
| `verbosity`         | integer 0–4                                     | `-v`…`-vvvv`                                   |
| `timeout`           | integer (seconds)                               | runner timeout                                 |
| `vault`             | `{credentialRef: string}`                       | `--vault-password-file` (file-injected, D4)    |

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
Gating is booked as a **follow-up via a content-blind mechanism**: the plugin declares a typed signal (a
`changeClass`, or a typed token on the existing non-precedence-bearing channel) that the PDP gates on —
never a tool-specific field.

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

### D3 — Content is an EE **build-time** declaration, pinned and hash-verified; the image digest is the run's single truth

Collections are declared where they are _resolved_ — in the **Execution Environment build declaration**
(the `ansible-builder`-compatible input the EE factory consumes), **not** as a field on the run-time
`ansible.input` Contract:

```yaml
# EE build declaration (consumed by the EE factory — parity P5), NOT ansible.input.v5
collections:
  - { name: community.crypto, version: "2.22.3" }
requirements: { scm: { ... }, path: requirements.yml } # or a content-ref to a requirements file
```

**Why content stays out of the run-time Contract.** A `content:` field on `ansible.input.v5` would be a
Contract field the runtime does not honor — the _identical_ defect D2 exists to correct, re-introduced two
decisions later. Worse, once the factory exists, a Step's declared pins and the digest-pinned image's
actual contents would be two assertions of one fact with no stated winner. **The EE image digest is the
single truth about what content a Run had** (§1.2 in spirit: one authority per fact). A Step selects
content by selecting its `eeImage` — a field that already exists and is already honored.

**The default posture is therefore that the EE image is the content boundary**: collections are installed
at build time with pinned versions and verified hashes, and the Run consumes that immutable, digest-pinned
image. Run-time `ansible-galaxy install` is **opt-in and explicitly non-default**; when enabled it requires
a declared source, a brokered credential, pinned versions, and hash verification — and, because it makes
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
  Build-time collection installation is one more subprocess in that layer writing *content* into the image,
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

## Consequences

- **Positive.** Unblocks the app-install-with-a-certificate demo (which needs `become` **and**
  `community.crypto`) and therefore the capstone chain. Closes the honest gap behind the 🟢 Controller
  parity score. Makes **privilege escalation a declared, reviewable, audited** value rather than an opaque
  flag string — with Control-gating a booked follow-up via a content-blind signal (D1), not a claim made
  today. Gives content a supply-chain story (pinned, hash-verified, digest-pinned EE)
  consistent with §7.3. Resolves PLG-1 for Ansible and sets the pattern for every later plugin.
- **Negative / trade-offs.** A v5 Contract plus a build-time content path is real work, and baking content
  into EEs pushes **EE proliferation** — which makes the `ansible-builder` **factory (parity P5) more
  urgent, not less**; until it exists, EE builds stay hand-rolled. Build-time resolution trades run-time
  flexibility for reproducibility (deliberate); teams wanting ad-hoc collections must opt into the
  non-default run-time path or rebuild an EE. **Air-gap seeding remains unsolved.**
- **Follow-ups.** (a0) **Redirect the AWX materializer** (`plugins/awx/materialize/workflows.go`) from
  `params["check"]` to `Step.DryRun` (D2) — the prerequisite for ever removing the deprecated field.
  (a) The `ansible-builder`-compatible EE factory (parity P5) — this ADR defines the
  contract it must satisfy. (b) Air-gap content seeding. (c) Survey → input-Contract enforcement on Steps
  (the named deferred item in ADR-0025/0026). (d) `/api/v2` route breadth + notification sinks (P2/P3) —
  separate ADRs. (e) Update [`aap-2.7-parity.md`](../aap-2.7-parity.md) when v5 + content land, and
  **narrow or close PLG-1** in `enterprise-readiness.md`. (f) A demo exercising the new knobs end to end —
  the app-install + certificate scenario is that demo, and per the demo-library experience
  ([ADR-0116](0116-demo-library.md)) it should be treated as the integration test for this work.

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
