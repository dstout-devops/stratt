# ADR 0088 — adopt-as-a-job: the credential-bearing deep-read + transform runs in a first-party execution pod

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian — PASS-WITH-CHANGES, then re-gated on the corrected §1.4 (MF-1: the first
  draft mis-cited `notify`/`mcp` as *in-tree* Actions; the dark-matter arc, ADR-0046/0052, already moved
  them onto the sovereign **port** and emptied the in-tree `a.Actions` registry — `strattd/main.go:434`,
  `:558-576`. This ADR is rebuilt on the true precedent: a **core-owned Action over the port**, not an
  in-tree binary). §2.5 locus, §1.2 Run-provenance, §1.6 use-without-read, §2.4 all confirmed clean.
  vocabulary-linter — PASS (`adopt/materialize`, `stratt-adopt`, `actions/adopt/*`, `credentialRef`,
  `nativeId` all clean; `token`→`credentialRef` is the charter-mandated rename).
- **Charter sections:** **§2.5** (secrets — the material now resolves ONLY at pod spawn, the canonical
  locus; no AWX material touches the long-lived control-plane process, even transiently), **§1.6**
  (agent-native, `use-without-read` — an agent Principal adopts by referencing a CredentialRef it may
  *use* but never *read*; adopt becomes a first-class descendable Run under one Principal / authz / audit
  / cost model), **§1.4** (boring spine — the transform stays CORE code in a CORE-owned job image, so the
  content-blind port does not bite), **§1.2** (the pod writes no projection graph; its product is a Git
  bundle captured as Run outputs, a permitted Run-provenance write), **§1.8** (adopt gains a real Run to
  descend into: events, status, outputs), **§5** (still no auto-launch — the emitted bundle is reviewed +
  merged; the adopted Workflow is Gated).
- **Supersedes-in-part:** ADR-0086 — the "Credential handling ruling (2026-07-19)" note recorded the
  transient-token synchronous path as the *accepted resting point* and named **Option D** as the future
  chartered path. This ADR **builds Option D and retires the in-core deep-read**: adopt is now always a
  job. ADR-0086's model — per-object, in-place, over the live projection; read-fidelity model (b);
  `observed→adopted` derived; `adopted-from` lineage; the explicit cutover — is unchanged; only the
  *locus* of the credential-bearing deep-read moves.
- **Builds on:** the `RunAction` targetless-Action path (§2.2, ADR-0031), the `notify/webhook` first-party
  in-tree pod Action (ADR-0040 — the exact precedent: a core-owned Action that runs in a pod with a
  CredentialRef injected at pod spawn by `RunAction.ResolveCredentials`), the `dispatch` K8s-Job machinery
  (credential mounts, typed streaming), and the pure `awximport.Bundle` transform (kept, unchanged).

## Context

ADR-0086 shipped `adopt` synchronously: the AWX read token rides the `POST /adoptions` request body, the
API server builds an `awx.Client` **in-process**, and `adopt.Adopt` does the targeted deep-read + transform
**in the control-plane goroutine**. The 2026-07-19 charter-guardian ruling accepted this as a resting point
(a *raw token in the body* is not a CredentialRef resolved in-core — the §2.5 locus rule was not violated),
but it has two costs the charter wants closed:

1. **The caller custodies raw material.** To adopt, a human or agent Principal must hold a live AWX token
   and put it on the wire. §1.6's `use-without-read` says a Principal should be able to *invoke* a
   capability that uses a credential it may never *read* — the notify Sinks already work this way (a Sink
   fires a CredentialRef its Principal has `use` on; it never sees the secret).
2. **Material transits the control plane.** Even transiently, the token is decoded into the long-lived,
   multi-tenant `strattd` process. The chartered guarantee (§2.5) is *structural about the locus*: material
   should resolve only in an execution pod, at pod spawn, confined + zeroed — never in the control plane.

The chartered-clean close is **Option D** (named in ADR-0086): run the credential-bearing work in a
first-party execution pod that resolves the AWX CredentialRef at **pod spawn**. The precedent is not
hypothetical — `notify/webhook` is already exactly this shape: a **core-owned Action served over the
sovereign port** (a gRPC plugin Action, `strattd/main.go:558-576`; the sibling `awsec2/create-vm` is the
same), dispatched via `RunAction`→`ExecuteAction`→`Host.InvokeRaw`. The `stratt-notify` pod resolves the
Sink's per-call material via the **SDK SecretBroker under its own confined RBAC** — the core hands only
COORDINATES in the Envelope, never material (§2.5); the plugin declares its input+output Contracts in its
Manifest, and the typed output rides back on `InvokeResult.Outputs`. Adopt-as-a-job is that exact shape
with the (kept, core-owned) `awximport` transform inside the pod.

One honest difference from `notify` sets adopt's module home: `stratt-notify` is a standalone SDK-only
module because its logic (an HTTP POST) has no core dependency. Adopt's Materialize runs
`awximport.Bundle` + the `awx` deep-read client, which live in `core/internal/` — core code (this is the
strangler-fig authority-taking act, spine not tool-breadth; the guardian confirmed running it in a
core-owned image is charter-permissible). So the adopt Action server lives in the **core module**
(`core/cmd/stratt-adopt`) and ships as a core-owned image, still speaking the port. "Core-owned plugin
over the port" is the accurate frame, not "extract adopt to a dark-matter plugin."

## Decision

**Split adopt into a core-side RESOLVE (a graph read; no credential) and a pod-side MATERIALIZE (the
credential-bearing deep-read + transform), and run MATERIALIZE as a first-party `RunAction` in an execution
pod with the AWX CredentialRef injected at pod spawn. Adopt becomes async: `POST /adoptions` launches a Run
and returns its id; the emitted bundle is the Run's typed output.**

### 1. The Resolve / Materialize split (who touches what)
- **`adopt.Resolve(ctx, catalog, req) → Resolved{NativeID, Source, Live[]}`** — runs in the control plane.
  It is entirely graph reads: resolve the object from the projection catalog (fail-loud if not observed,
  ADR-0086 §1), and enumerate the still-live foreign-side executions for the cutover guard. **No credential,
  no AWX I/O.** This is the part that legitimately belongs in-core (it reads OUR graph).
- **`adopt.Materialize(ctx, reader, req, resolved) → Emit{Files, Report}`** — runs in the pod. Given a
  `Reader` (an `awx.Client` built from the SecretBroker-resolved token) and the already-resolved coordinates, it does
  the targeted read-only deep-read (definition-truth; fail-loud if gone at read-time), runs `awximport.Bundle`,
  stamps `adopted-from` lineage, and appends the cutover-guard report from `resolved.Live` (no longer a
  catalog callback — the live set was resolved core-side and passed in the request). Materialize depends on
  **only** a Reader + the resolved coordinates — it never reaches back into the graph.
- `adopt.Adopt` (Resolve ∘ Materialize) is retained as the composition used by unit tests and any in-process
  caller; production goes through the two halves across the pod boundary.

### 2. The job: a core-owned Action `adopt/materialize` served over the sovereign port
Registered exactly like `notify/webhook` and `awsec2/create-vm` — a **gRPC plugin Action** (`a.PluginActions`
via `registerPluginAction`), dispatched by the existing `RunAction`→`ExecuteAction`→`Host.InvokeRaw` path
(zero new orchestration; the plugin branch already validates the output contract and returns typed
`raw.Outputs`). It is a **core-owned** plugin: the server binary lives in the core module (it imports the
core `awximport`/`awx`), and ships as a core-owned image. This does NOT re-open the in-tree `a.Actions`
registry the dark-matter arc emptied (`strattd/main.go:434`).
- **Input Contract `actions/adopt/materialize.input`** (data, hash-verified §1.5): `{kind, identity,
  endpoint, nativeId, source, live[], credentialMount}` — the core-resolved coordinates + the CredentialRef
  NAME. Validated at the launch door (`ValidateActionInput`).
- **Output Contract `actions/adopt/materialize.output`**: `{files: {<path>: <content>}, report: <string>}`
  — the emitted bundle, carried on `InvokeResult.Outputs` and validated (`ValidateActionOutput`) before it
  is recorded (§2.2: an Action that lies about its outputs fails the Run; the plugin's asserted output
  contract id must match the core-pinned id — the existing drift check).
- The plugin declares the Action + its Contract refs in `GetManifest` (Class ACTION, Verb INVOKE), exactly
  like `notify` (`plugins/notify/server.go:45`).
- **`stratt-adopt`** — a new Go binary at `core/cmd/stratt-adopt` (Apache-2.0, pure Go; core module, so it
  can import `core/internal/adopt` + `awximport`; no GPL linkage). It serves `PluginService`: its `Invoke`
  decodes the args, resolves the AWX token via the SDK `SecretBroker.WithMaterial` (in-pod, confined RBAC,
  zeroed after use — the `notify` MF-A/MF-B pattern), builds `awx.New`, calls `adopt.Materialize`, and
  returns `Emit{Files, Report}` on `InvokeResult.Outputs`. The token exists only inside this pod's
  use-closure, is never logged, and is zeroed on return (§2.5).

### 3. The credential contract flips: `credentialRef`, not a raw token (§1.6 use-without-read)
`POST /adoptions` no longer accepts `token`. It accepts a **`credentialRef`** name (an AWX token stored as a
`k8s-secret` CredentialRef, ADR-0009). Authz gains a second gate beside the existing `adopt`-on-Source grant:
the standard **`use` grant on `credential_ref:<name>`**, enforced by `RunAction.ResolveCredentials` and
audited on the one stream (§1.6) — the identical chokepoint every notify delivery and Action Run uses.
`ResolveCredentials` reads only the ref's locator (namespace/name COORDINATES), never material; the
coordinates cross to the `stratt-adopt` pod in the Envelope, and the pod's SecretBroker resolves the token
under its own RBAC at invocation. The caller/agent references the credential by name and never custodies
material: `use-without-read`.

### 4. Async + result capture (§1.2/§1.8)
Adopt is now a Run. `POST /adoptions` does the graph-read Resolve, then `LaunchRun` with `Action:
"adopt/materialize"` and `CredentialRefs: [name]`, and returns **202 Accepted** with the Run id. The pod's
emitted bundle is captured by `RecordActionResult` into the Run's typed outputs (`SetRunOutputs` — Run
provenance, a §1.2-permitted write; NOT a projection Entity attribute, NOT a second truth — it is the Run's
product, which the operator then commits to Git). **`GET /adoptions/{runId}`** returns the Run status and,
on success, `{files, report}`. Progress descends via the standard RunEvent stream (§1.8). Large-bundle
sealing into the evidence/bundle object store (content-addressed, cosign-signed) is a noted future hardening;
the jsonb output channel is correct for the small YAML bundles adopt emits today.

### 5. What is retired, what is unchanged
- **Retired:** the in-core synchronous deep-read — `awx.New(...)` in the API handler and the raw `token`
  request field. No AWX material path remains in `strattd`.
- **Unchanged:** ADR-0086's whole model (per-object, in-place, live projection; read-fidelity (b);
  `observed→adopted` derived; `adopted-from` lineage; explicit cutover; §5 no auto-launch; API-first,
  agent-native). The standing cutover reconciler (ADR-0087) reads the same structured `adoptedFrom` on the
  emitted Workflow — untouched. `awximport.Bundle` — untouched.

### Cost accepted
Adopt now requires the execution substrate (Temporal + the core-owned `stratt-adopt` image reachable over
the port) — it no longer runs in a bare API process. This matches every other execution in the platform (a
Run over the port) and is consistent with the notify path; pure-no-cluster dev loses the synchronous
shortcut. Accepted: adopt is a rare, deliberate operation, and it *gains* a first-class descendable Run,
audit, and cost accounting (§1.6/§1.8) — the trade the charter wants.

## Alternatives considered
- **Keep the transient-token sync path as a dev escape hatch alongside the job path.** Rejected: two credential
  paths means the raw-token custody the charter is closing never actually goes away, and "use-without-read"
  becomes optional rather than the model. One path.
- **(Option A) Resolve the CredentialRef in-core via the SecretBroker.** Rejected by the ADR-0086 ruling —
  a §2.5 locus violation (no CredentialRef-material path may exist in the long-lived control plane).
- **(Option B) Route the deep-read through the existing `plugins/awx` Syncer plugin over the port.** Rejected
  — the content-blind Syncer projects a lean catalog; it does not carry the rich job-template/survey/
  credential/node detail the transform needs (more-signal->less), and the transform (`awximport.Bundle`) is
  core code, not the plugin's. See the F-1 note below on the resulting two-AWX-client tension.
- **A clean SDK-only dark-matter plugin (extract adopt to `plugins/adopt`).** Deferred, not rejected: it
  would require moving `awximport` + the `awx` deep-read client out of `core/internal/` into a shared,
  SDK-importable module — a large extraction touching the sync API, `awxfacade`, and the awximport tests. Out
  of scope for this slice; the core-owned-image framing (Decision §2) is the correct near-term, and the
  guardian confirmed it charter-permissible.
- **An in-tree `a.Actions` pod Action.** Rejected — the dark-matter arc (ADR-0046/0052) deliberately emptied
  the in-tree Action registry (`strattd/main.go:434`); re-opening it would fork against "every tool over the
  port." The core-owned server speaks the port instead.
- **A bespoke one-off Job dispatch (bypass RunAction).** Rejected — `RunAction` already gives the Run record,
  the §2.5 credential resolution+audit, typed output validation, cancellation, and descent. Inventing a
  parallel single-job launch would duplicate all of it.

## Charter alignment
- **§2.5:** AWX material resolves ONLY inside the `stratt-adopt` pod — its SDK SecretBroker dereferences the
  Envelope-carried coordinates under the pod's own confined RBAC, inside a use-closure, zeroed on return
  (the `notify` MF-A/MF-B pattern); nothing in `strattd`. The locus guarantee is now structural for adopt.
- **§1.6:** `use-without-read` (reference a CredentialRef, never read it); one Principal / authz / audit /
  cost; agent Principals can adopt exactly as humans do; API-first, CLI/MCP/UI are clients.
- **§1.4:** the spine names no tool — `adopt/materialize` is dispatched by the tool-blind `RunAction` path
  over the sovereign port, byte-identically to `notify/webhook`. The transform is core code, so its server
  is a core-owned image (not a third-party plugin); the port carries opaque typed Contracts (input/output),
  so the port itself stays content-blind. No `if <tool>` enters the spine.

  **F-1 (acknowledged tension): two AWX clients.** `core/internal/awximport/awx` (core, used here) and
  `plugins/awx` (the Syncer over the port) both speak AWX. This ADR bakes the core one into `stratt-adopt`,
  deepening the split. It is deliberate: the transform needs richer detail than the content-blind Syncer
  port carries. Converging AWX I/O onto the plugin is the "clean dark-matter plugin" alternative above —
  a future refactor, explicitly out of this slice.

  **F-2 (acknowledged cost): the substrate floor.** Retiring the sync path raises adopt to full substrate
  (K8s + Temporal + the core-owned adopt image) for what ADR-0086 framed as a lightweight per-object act.
  Accepted (§6 above): dev already deploys plugins via Helm (the "dev env = prod baseline" posture), adopt
  is rare, and it gains a first-class descendable Run. One credential path beats keeping raw-token custody
  alive as an optional escape hatch.
- **§1.2:** the pod writes no projection graph; the bundle is desired state captured as Run outputs (Run
  provenance), which the operator commits to Git — never a projection attribute, never a second truth.
- **§1.8/§5:** adopt is a descendable Run; the emitted bundle is reviewed + merged; the Workflow is Gated.

## Slice roadmap
1. **This ADR + core (this slice):** the Resolve/Materialize split; the `stratt-adopt` binary; the
   `adopt/materialize` Action + input/output Contracts; the async `POST /adoptions` (credentialRef → Run) +
   `GET /adoptions/{runId}`; CLI async client; MCP surface update.
2. **Future hardening (own slices):** seal large bundles into the evidence/bundle object store (signed,
   content-addressed) instead of jsonb; extend beyond `ansible.template` to workflow-job-templates and other
   adoptable kinds (same split, more Materialize cases).
