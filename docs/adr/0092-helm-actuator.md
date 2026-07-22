# ADR 0092 — Helm Actuator: chart → release behind Gates, over the sovereign port

- **Status:** **Accepted** (2026-07-22) — steward sign-off on the §8 Phase-4 pull-forward; full Helm
  Actuator authorized. All three review gates cleared (vocabulary-linter CLEAN, dependency-scout
  RECOMMEND, charter-guardian PASS); the two charter-guardian hardenings and the dependency-scout
  Helm-4 targeting are folded into the Decision.
- **Date:** 2026-07-22
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.1 (type the seams), §1.5 (sovereign contracts, multiple transports), §1.8
  (never hide diagnosis), §2.3 (Actuator), §7.1 (any-Kubernetes self-host), §7.3 (pinned digests /
  supply chain), §8 (Phase 4)
- **Frames under / sharpens:** [ADR-0083](0083-blueprint-route-materialization-seam.md) (discharges
  the `helm-actuator` route target), [ADR-0046](0046-stratt-as-substrate.md) /
  [ADR-0047](0047-plugin-port-v1-full-surface.md) (sovereign plugin port), [ADR-0016](0016-opentofu-actuator.md)
  (OpenTofu Actuator — the direct mirror), [ADR-0058](0058-provisioning-from-intent.md) (helm as a
  gated materializer of declared outcomes)

## Context

ADR-0083 named the Blueprint route `{ app: helm-actuator }` as *the* tool-materialization seam, but
**no Helm Actuator exists** — only `script` and `opentofu` can shell out to a tool today (per the
current post-ADR-0046 plugin surface; `plugins/opentofu` runs the `tofu` binary as a subprocess over
the sovereign port). So "materialize a declared outcome as a Kubernetes release" — the single most
common cloud-native deploy shape — has no first-class home.

Two shipping consumers need it now:
1. **Stratt's own self-deployment** (the M0 bootstrap already landed; M2 will have Stratt deploy its
   own plugin workloads): Stratt must install/upgrade Helm releases *through its gated
   reconcile → build → Actuator loop* (ADR-0058), not via imperative `task` helm calls.
2. The general "deploy an app to Kubernetes" frontier ADR-0083 designed the route map for.

The pattern is proven and this ADR deliberately mirrors it: **OpenTofu** (ADR-0016) is the same
shape — a tool that owns release/state, `plan`/`apply` modes, a human Gate between them, streamed
diagnostics. Helm is that shape exactly (`template`/`apply`, release state held in-cluster).

**Phase note (honest):** Helm/Packer Actuators are listed under charter **§8 Phase 4** (roadmap),
and Phase 3 is not complete. This ships the Helm Actuator early, justified by unlocking self-deploy
and the outstanding **live-cluster e2e** — and kept minimal (mirror opentofu; no destroy verb in v1).

**In scope:** a `plugins/helm` gRPC Actuator, its input Contract, template/apply modes, interpret,
and the RBAC posture. **Out of scope:** a true resource-level diff (`helm diff` / server-side-apply
dry-run), the `uninstall` verb, and release-status → Entity projection — each a named follow-up.

## Decision

1. **`plugins/helm` — a gRPC Actuator over the sovereign plugin port** (ADR-0046/0047), mirroring
   `plugins/opentofu`: it shells out to the `helm` binary as a subprocess (transport beneath the
   sovereign contract, §1.5). Registered in the `pluginActuators` map (`core/cmd/strattd/main.go`) and
   deployable in-cluster via the chart's `plugins.yaml`. Core stays content-blind — it hands helm the
   opaque chart + `values` payload and governs the envelope.

2. **Input Contract `actuators/helm.input`** (rung 1, hand-written — §2.2): `chart` (OCI ref, or
   repo+name — **pinned by digest or exact version**, §7.3), `release`, `namespace`, `mode`
   (`template`|`apply`), `values` (an **opaque object** overlay — validated only as an object, never
   accreting per-chart fields into Stratt's Contract, §1.1), and optional `repo` / `version` /
   `createNamespace`. `values` is validated against the *chart's own* schema downstream by helm, not
   by the Stratt Contract — the §1.1 "type the seam, not the world" line.

3. **Two modes, a Gate between them** (mirror ADR-0016 §6): `template` = `helm template` — read-only,
   renders the manifests that *would* apply, streamable (the plan-equivalent). `apply` =
   `helm upgrade --install --rollback-on-failure --wait` — mutating, belongs **behind a Gate** (§5,
   no silent auto-apply). `template` and `apply` are two Steps of one Workflow sharing (release,
   namespace). **Flags are Helm-4 spelling** (`--rollback-on-failure`, not v3's `--atomic`; SSA is the
   v4 default) — see the version-targeting decision below.

3a. **Target the Helm 4 line** (dependency-scout, 2026-07-22): pin the Actuator's subprocess to
   **helm v4.2.3** (current), with **v3.21.3** as the N-1 that must keep working. **Not** the
   `3.16.4` the repo vendors for chart packaging — Helm 3's bug-fix support lapsed 2026-07-08 and its
   security EOL is 2026-11-11, so a *newly load-bearing* runtime pin to old v3 would violate the
   evergreen contract (§1.7) on day one. Helm 4 keeps Chart API v2 compatibility (existing charts run
   unmodified); only CLI flag spellings changed (handled in the invoke shim).

3b. **`mode: apply` is a Contract-declared *mutating* capability — the Gate is compile-enforced, not
   author-trusted** (charter-guardian §5 hardening). OpenTofu has a structural fail-closed guard (no
   state key ⇒ the actuator refuses to register); helm has no natural refuse-condition, so the Gate
   must not depend on a Workflow author remembering to place one. The `helm.input` Contract marks
   `mode: apply` **mutating**, and the ADR-0058 §4.3 / admission machinery **requires a preceding Gate
   for a mutating Step at compile time** — closing the "author forgot the Gate" hole. `template` is
   non-mutating and needs no Gate.

4. **No `uninstall` / destroy verb in v1** — the most dangerous verb arrives with its own review,
   exactly as OpenTofu deferred `destroy` (ADR-0016 follow-ups).

5. **Interpret (§1.8 — never hide diagnosis):** helm stdout/stderr lift to typed event kinds — the
   rendered manifests ride a `template` event; `helm upgrade` progress + hook/`--wait` status →
   `apply_start` / `apply_complete`; every helm error → a `diagnostic` event (severity + summary +
   detail), never swallowed. Terminal mapping: template-ok → `ok`, apply-ok → `changed`, rc≠0 →
   `failed`. (A real resource-level diff — `helm diff` or an SSA dry-run — is a recorded follow-up;
   v1's reviewable artifact is the rendered `template`.)

6. **RBAC — a per-route scoped ServiceAccount is the *required* model (§7.3, charter-guardian
   hardening):** the process that runs `helm apply` needs in-cluster permission to create the
   release's resources. The required model is a **per-route / per-namespace ServiceAccount + Role
   declared on the Blueprint route** — never one ambient broad plugin SA, never cluster-admin — so an
   "any-chart" deploy cannot inherit broad create rights across all kinds. The **broad-SA path is
   admissible only for the core-tier self-deploy dogfood** (M2), scoped to its target namespace;
   community-tier helm routes inherit the §2.3 trust-tier + §7.3 sandbox-by-default posture.

7. **Chart integrity (§7.3):** production routes pin the chart by OCI digest (or exact version +
   provenance), never a floating chart tag — the chart-side mirror of the image-digest posture — and
   **verify the chart's cosign signature / `.prov` provenance alongside the digest** (charter-guardian
   hardening). lint/NOTES warn on an unpinned or unverified chart.

8. **Dual surface — the complete plugin ships BOTH an Actuator and an Action**
   (amendment 2026-07-22, steward direction "most complete and proper"; the live
   integration exposed the gap). helm has two legitimate uses and the exemplar is
   `crossplane` (Apply + Invoke + Observe — the full-featured plugin, ADR-0060):
   - **`helm/deploy` Action** (`Invoke`) — deploy ONE release to ONE named namespace,
     **targetless** (the `RunAction` launch path needs no View). This is the
     self-deploy / single-release build path, mirroring `crossplane/provision`. It
     exists because an `actuator:` Run **requires a View resolving to ≥1 Entity**
     (`ResolveTargets` fails on an empty View — Actuators are per-target), which a
     cluster-scoped single deploy has no host to satisfy.
   - **`helm` Actuator** (`Plan`/`Apply`) — deploy a release to **each** target in a
     View (fleet / multi-namespace / multi-cluster rollout — the ADR-0083 route-to-a-
     group). **Refinement (follow-up):** the v1 Apply is release-scoped (item_key "");
     true per-target fan-out needs a target→namespace/release mapping (each View
     Entity is a namespace/cluster) — the fleet-deploy completion, its own slice.
   Both reuse one plugin + one grant; contracts `actions/helm/deploy.{input,output}`
   (Action) and `actuators/helm.input` (Actuator).

## Charter alignment

- **§2.3 Actuator (not Action):** helm interprets *tool content* (a chart) and produces many effects
  (the release's resources) — textbook Actuator, exactly like ansible/opentofu.
- **§1.5 sovereign port, transports beneath the contract:** a gRPC plugin with a pinned, hash-verified
  Contract; the `helm` subprocess is a transport, never load-bearing for the deterministic core.
- **§1.1 type the seams, not the world:** `helm.input` types the seam; `values` and the chart stay
  opaque pass-throughs (helm owns their meaning) — no per-chart ontology accretes into Stratt.
- **§1.8 never hide diagnosis:** helm errors become typed `diagnostic` events; the
  Intent → Blueprint route → Workflow → Run → task-event descent survives the port.
- **§7.1 any-Kubernetes:** helm is K8s-native and vendor-neutral — no OpenShift/vendor-only path.
- **ADR-0083:** discharges the named `helm-actuator` route target (declare the outcome; the plugin
  materializes the tool-specific state).
- **Permanent non-goals:** upholds them — helm deploys to Kubernetes (not OS imaging / bare-metal),
  introduces no new configuration language (chart/values are the tool's own, opaque to core).
- **Tension (must surface):** this is charter **§8 Phase-4** work started while Phase 3 is ~90%.
  Justified by unlocking self-deploy + live-cluster e2e; scoped minimal. Steward sign-off required.

## Consequences

- **Positive:** "declare an app → gated build → helm materializes it" exists; Stratt can deploy
  Kubernetes workloads (including its own plugins — M2) through the reconcile loop it already owns; the
  ADR-0083 route map gains its first non-config materializer; self-deploy (M1) unblocks.
- **Negative / trade-offs:** a new runtime tool dependency (`helm` — see the dependency-scout review)
  with K8s-version-skew tracking (§1.7); broad in-cluster deploy RBAC is a real surface (scoped, not
  eliminated); helm's `apply` is less structured than tofu's `-json` plan, so v1's "diff" is the
  rendered `template`, not a resource-level diff.
- **Follow-ups:** `helm diff` / SSA dry-run for a true resource diff; the gated `uninstall` verb
  (own review); release-status → Entity projection via a Normalizer (the provision→observe seam);
  per-route scoped-ServiceAccount model as the general RBAC path (charter-guardian) + cosign/`.prov`
  chart verification. **Evergreen (§1.7, dependency-scout):** bump `Taskfile.yml:HELM_VER` off the
  stale `3.16.4`; add checksum + GPG-signature verification to the `tools:helm` install (today a bare
  `curl | tar`, a live §7.3 gap); add an N-1 upgrade-smoke job running the plugin's helm invocations
  against **both** v4.2.x and v3.21.x binaries; calendar the **2026-11-11** Helm-3 security-EOL to
  roll the N-1 pin forward.

## Alternatives considered

- **`script` Actuator wrapping `helm`** — rejected as the durable home: no typed Contract, no
  template/apply/Gate structure, no interpret/diagnostics. Fine for a one-off spike, wrong for the
  named ADR-0083 route target that many Blueprints will pin.
- **`opentofu` Actuator with the Helm provider** — rejected: drags tofu state + the provider's own
  release bookkeeping into every k8s deploy; heavier and less direct than `helm upgrade`. The two
  stay separate route targets (co-management is reality — ADR-0083 §3).
- **Raw `kubectl apply` Actuator** — rejected for release-shaped deploys: no release lifecycle, no
  values templating, no upgrade/rollback. A thin `kubectl` actuator for *non-chart* manifests may
  arrive later on its own merits.
- **Linking the Go Helm SDK (`helm.sh/helm/v3`) instead of the binary** — rejected for v1: it links
  helm's large dependency tree into a plugin and breaks the arm's-length subprocess pattern every
  other Actuator uses (§1.5). Helm is Apache-2.0, so there is no license barrier — this is purely a
  consistency/footprint call; revisit if subprocess overhead ever matters.

## Reviews

- **charter-guardian (2026-07-22): PASS — with steward sign-off + two hardenings (folded).** No hard
  violations; all eight Founding Disciplines and five permanent non-goals upheld (content-blindness on
  the Apply path correct; helm's release state is its own in-cluster SoR, no phantom Entity; §1.1
  opaque `values` pass-through clears the sufficiency gate as a shipping ADR-0083 consumer). Two
  strengthenings folded above: (a) `mode: apply` is a Contract-declared *mutating* capability so the
  Gate is **compile-enforced** not author-trusted (decision 3b); (b) **per-route/per-namespace scoped
  ServiceAccounts** are the required RBAC model + cosign chart verification (decisions 6–7). **§8
  timing (Phase-4 work while Phase-3 is ~90%) is a steward judgment call — flagged, not reckless**
  (`helm` is already a charter-named Actuator §2.3; the pull is genuine — self-deploy + live e2e are
  blocked without it; scope is minimal). Requires explicit steward sign-off before merge.
- **dependency-scout (2026-07-22): RECOMMEND — target the Helm 4 line.** Apache-2.0, CNCF-graduated,
  DCO (no rug-pull risk); quarterly SemVer cadence on both the current and N-1 major simultaneously;
  K8s skew N-3 (exceeds the §1.7 N-1 floor). **Binding: pin helm v4.2.3 (current), N-1 v3.21.3 — not
  the repo's stale v3.16.4** (Helm-3 bug-fix support lapsed 2026-07-08; security EOL 2026-11-11), so
  the invoke shim uses v4 flag spellings (`--rollback-on-failure`, SSA default). **Second binding
  condition:** `tools:helm` currently does a bare `curl | tar` with no integrity check — add checksum
  (min) + GPG-signature (target) verification. Both folded into decision 3a + Follow-ups.
- **vocabulary-linter (2026-07-22): CLEAN.** No §2 violations. New identifiers (`helm` actuator id,
  `actuators/helm.input` + fields, `template`/`apply` modes, `diagnostic` events) are consistent with
  the `opentofu`/`ansible` actuators; `resource` appears only as Helm's own tool vocabulary
  (Kubernetes resources), permitted in a plugin Contract (§1.1/§2.2), never a core-model identifier.
