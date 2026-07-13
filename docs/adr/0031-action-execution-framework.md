# ADR 0031 — Action-execution framework (+ provision→configure seam)

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.2 (Connector capabilities — Syncer/Action/Emitter),
  §2.3 (Step = Actuator+content OR Action+params; the Action/Actuator split is
  "deliberate and permanent"; the Workflow seam feature: provision→configure in
  one graph), §1.1 (type the seams), §1.2 (projections), §1.4 (boring spine),
  §2.5 (CredentialRef), §1.6 (one authz/audit), §1.8 (never hide failure);
  ADR-0017 (tofu outputs→Entities), ADR-0024 (param templating), ADR-0028
  (View-scoped execution), ADR-0030 (cert-issuer — the flagged Actuator)

## Context

The charter defines an **Action** as "one typed operation (create-vm,
assign-policy, revoke-cert): input Contract + **output Contract** +
idempotency/dry-run declaration" — a Connector capability. A Step is "(Actuator +
content + params) OR (Action + params)", and the Action/Actuator split is
"deliberate and permanent — their contract, drift-check, and sandboxing semantics
differ." Until now the Action shape did **not exist**: cert-issuer and webhook
were typed operations *dressed as Actuators*, which charter-guardian flagged
(ADR-0030) as disclosed drift against a permanent distinction, on its **second**
capability. Building Actions now — before Sites and more Connectors pile onto the
wrong seam — retires that debt and shapes later slices.

## Decision

1. **Action is a first-class type, structurally distinct from Actuator (§2.3).**
   New `actions.Action` interface (`Name` namespaced by Connector, `Idempotent`,
   `DryRunnable`, `Prepare(params, dryRun)`, `Interpret`) — separate from
   `actuators.Actuator`, with its own **registry**, its own **Step shape**
   (`Step.Action`, mutually exclusive with Gate/Actuation — a three-way validation),
   its own **contract namespace** (`contracts/actions/<connector>/<op>.input|output`),
   and its own **`RunAction` Temporal workflow**. What they share is the pod
   execution path: both satisfy `dispatch.Interpreter`, so the Dispatcher runs
   both with no parallel stack (§1.4). Actions are **targetless** (no View).
2. **Input AND output Contract (§1.1, §2.2) — the defining feature.** An Action
   validates its params against `actions/<name>.input` at the door, and its
   **produced outputs against `actions/<name>.output`** post-run
   (`ValidateActionOutputs`) — the net-new validation direction that makes an
   Action more than an Actuator. A mismatch fails the Run (§1.8). Dry-run plans
   skip output validation (a plan is not the contracted output).
3. **Idempotency + dry-run declarations (§2.2).** `Idempotent()` (revoke = yes;
   issue/renew/create-vm = no) and `DryRunnable()` are first-class; `dryRun`
   requests a side-effect-free plan (the opentofu `mode: plan` pattern —
   certissuer skips the CLM write; create-vm asks EC2 `DryRun`). Activity-retry
   safety comes from Job-name adoption (`stratt-run-<runID>-s0`); launch-level
   dedup via a stable workflow-id for idempotent Actions is a documented follow-up.
4. **Targetless authz chokepoint (§2.5, §1.6).** Actions are not View-scoped, so
   ADR-0028's `runner`-on-View grant does not apply; `RunAction` gates on the
   `ResolveCredentials` **`use`-check** — proven live (cert renew denied until
   `use` on `credential_ref:cert-issuer`). An Action-object `run` grant (parallel
   to ADR-0028's deferred `run`-on-View type) is future work; every v1 Action
   carries credentials, so `use` gates them all, and launches are authenticated
   (401-never-anonymous holds). To keep that invariant **structural rather than
   assumed** (guardian §1.6 flag), `ExecuteAction` refuses a credential-free
   Action (`ActionUngated`) — so a future creds-free Action cannot silently
   become an ungated execution path before the `run`-grant lands.
5. **Reframe cert-issuer → Connector Actions (retires the ADR-0030 flag).** The
   write ops are now `certissuer/{issue,renew,revoke}` on the certissuer
   Connector, each with its own in/out Contract; the `cert-issuer` **Actuator is
   deleted** (removed from the registry, the StartRun enum, and the codebase).
6. **create-vm + the provision→configure seam (§2.3 seam feature).**
   `awsec2/create-vm` provisions a moto instance via a small **Go EE driver**
   (`cmd/actions-ec2`) reusing the vendored `aws-sdk-go-v2` (no boto3, no new Go
   dep; §1.4) in `stratt-ee-actions`. It projects the new instance as an Entity
   with **Run provenance** (§1.2 — the ADR-0017 path, Action-typed) and emits typed
   outputs. A new **`{{.steps.<name>.outputs.<field>}}`** template namespace
   (`ResolveStepParams`/`ResolveActionStepParams`) binds a Step's outputs into a
   downstream Step — provision→configure in one graph, one RBAC model, one audit
   stream. Outputs are stored on the Run (`graph.run.outputs`, migration 00017)
   and exposed on `GET /runs/{id}` and the `get_run` MCP tool.

## Consequences

- **Live-verified (dev harness: OpenBao + moto + kind + EE):**
  - cert lifecycle **via Actions**: `certissuer/revoke` **dry-run** succeeded and
    left the cert live (revocation_time 0 — no side effect); a real revoke
    returned typed outputs `{serial, revocationTime}` validated against the output
    Contract and stored on the Run, and revoked the cert in OpenBao; the
    `cert-renew` **Workflow Action-Step** re-issued the cert with `{newSerial}`
    captured.
  - **provision→configure seam**: the `provision-configure` Workflow ran
    `awsec2/create-vm` → a real moto instance `i-ff632…` (independently confirmed
    by the awsec2 Syncer finding it in moto and projecting it) → outputs
    `{instanceId, privateIp}` → the downstream configure Step's script received
    **`configuring new instance i-ff632…`** via `{{.steps.provision.outputs.instanceId}}`.
  - **§2.5**: the CLM token and AWS creds never appear in strattd logs; the retired
    `cert-issuer` Actuator now 400s; an uncontracted action 400s at the door.
- **Retires the ADR-0030 guardian flag**: `revoke-cert` is a true §2.2 Action.
- Contract count: 22 embedded documents (removed `actuators/cert-issuer.input`;
  added 6 `actions/certissuer/*` + 2 `actions/awsec2/*`).

## Deferred / fast-follow (documented)
- An Action-object `run` grant type (finer authz than the credential `use`-check);
  launch-level stable-workflow-id dedup for idempotent Actions.
- webhook-as-Action (the other ADR-0030-flagged Actuator-in-disguise; it is the
  notify Sink transport, not a Connector Action — reframe later).
- Trust tiers (core/verified/community) — still unimplemented everywhere; and the
  Python plugin SDK (v1 Actions are in-tree Go).
- Output binding into parametrized-View `ViewParams` (v1 binds into Step params;
  the label-selection handoff from ADR-0017 remains for tofu).

## Runway after
Phase-3 board continues: Sites (NATS leaf) + pull agent/Bundles; CIS pack;
audit→Splunk; HA/DR; SCIM.
