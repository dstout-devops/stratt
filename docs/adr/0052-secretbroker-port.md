# ADR 0052 — The SecretBroker port: per-call credential resolution for plugins (§2.5)

- **Status:** **Accepted** (2026-07-17, steward) — charter-guardian §2.5 design review (SOUND-WITH-CHANGES, four
  must-fixes folded into the Decision) + explicit steward sign-off on flag **F-3**: the conscious acceptance of
  the departure from the *literal* §2.5/§3 pod-injection model to a long-lived plugin executor (the §2.5
  *property* — core never holds material — is preserved; the blast-radius trade is mitigated by MF-A/MF-B). This
  ADR does **not** edit the charter; it records the decision at the §2.5 bar.
- **Date:** 2026-07-17
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.5 (secrets brokered, never held by the core) · §1.5 (sovereign contracts,
  multiple transports) · §1.6 (one Principal/authz/audit) · §2.2 (the CredentialRef Named Kind) · builds on
  ADR-0046 (SecretBroker named as a capability class), ADR-0009 (identity/authz), ADR-0047/0048 (the plugin
  port), ADR-0051 (the EE-Job transport).

## Context

Extracting the last in-tree domain logic (ADR-0046 dark-matter, Category A) hit a real wall at
**notify/webhook**. A long-lived gRPC **plugin Action** needs the delivery **url/token** of *the specific Sink
being notified* — a **per-call** credential that varies every invocation. The sovereign port has no channel for
this:

- The Envelope carries `CredentialRef` **names only** (`pluginhost` sends `{Name: n}`, never material) — the
  §2.5 seam across the process boundary.
- The one shipped gRPC plugin that touches secrets (**awsec2**) sidesteps the problem: it holds **one static
  cloud credential** in its own process env. That works for a single account; it does **not** generalize to a
  plugin that must resolve *a different secret per call* (notify, and any future per-target-credential plugin).
- ADR-0046 **names** `SecretBroker` as one of the capability classes (`StateStore`, `EventBus`, `SecretBroker`,
  `DurableExec`, `ArtifactStore`) but leaves it **unbuilt**.

The load-bearing constraint is the **strongest §2.5 property the platform has today**, which this ADR must not
weaken: **the core never holds credential material, even transiently.** For an EE-Job/pod, the core writes a
`secretKeyRef` into the pod spec and the **kubelet** — trusted infrastructure — resolves the Secret *into the
isolated pod*. The core's memory never contains the url/token. `ResolveCredentials` only ever produces Secret
**coordinates** (`SecretName`, `SecretNamespace`, the per-key `Injection`) after a **use-check + audit**; the
material resolution happens at the executor, not the core.

**In scope:** the port contract by which a plugin resolves a *granted* CredentialRef to material at use time,
preserving "core never holds material," plus the SDK resolver and the notify/webhook extraction that motivates
it. **Out of scope:** a Vault/cloud-KMS backend (a later pluggable resolver), rotating/short-lived credential
issuance, and the other four capability classes.

## Decision

Add the **SecretBroker port** as a §2.5 contract with a deliberately **core-material-free** shape: **the core
resolves a granted CredentialRef to Secret *coordinates* (never material) and hands those to the plugin in the
Envelope; the plugin resolves the material itself, at use time, from a backend confined to the granted refs —
mirroring the kubelet's role for pods.** Material never crosses the core. Concretely:

1. **Envelope enrichment (coordinates, not material).** `pluginhost` already runs the use-check + audit and
   holds each granted ref's `CredentialMount` (Secret name/namespace/keys). The Envelope's `CredentialRef`
   gains an optional **`ResolvedRef`** sub-message carrying those **coordinates only** — `secret_namespace`,
   `secret_name`, and the key map — for exactly the refs the call is authorized to use. No `bytes` field, ever.
   A plugin that receives no coordinates (a Site relay that must not learn them, or an unresolved ref) gets the
   name alone and cannot resolve — fail-closed.

2. **Plugin-side resolution with STRUCTURAL ephemerality (the SecretBroker SDK capability — MF-B).** The plugin
   **SDK** ships a `SecretBroker` resolver: given a `ResolvedRef`, it reads the K8s Secret (its own confined RBAC,
   MF-A) and returns material **in the plugin process only**. Because the executor is now long-lived (not a
   per-run ephemeral pod), ephemerality is enforced by the SDK **mechanism, not by guidance** (§1.2's "in the
   data layer, not by convention", applied to material): the resolver resolves **per-`Invoke`**, **zeroizes the
   material immediately after the single use**, **never caches across calls**, and **scopes resolved material to
   the invoking Principal** within a multiplexed process — so a long-lived plugin never accumulates the union of
   past callers' grants in memory. This is the kubelet role, generalized to a long-lived process, with the pod's
   one-Secret/one-Run/one-identity scoping reconstructed structurally. The default resolver is K8s-Secret-backed;
   the interface is pluggable (a Vault resolver is a later backend) — realized as an **SDK-side resolver over a
   core-provided coordinate**, not a material-serving core RPC.

3. **Authz + confinement (RBAC must approximate the use-grant, not exceed it — MF-A).** The **use-check remains
   the sole chokepoint** (§2.5, ADR-0009): the core only enriches refs the principal is `use`-authorized for, and
   audits each — identical to the pod path (`ResolveCredentials`). But a plugin's *standing* Secret-read RBAC
   must not be a **superset** of any single call's use-grant, or a compromised plugin could read Secrets its
   caller was never granted, and the use-check would no longer be the sole chokepoint. Therefore each plugin's
   Secret-read RBAC is confined to a namespace/set containing **only that plugin's brokerable Secrets** (a
   dedicated per-plugin Secret namespace, or per-Secret RBAC) — so the RBAC gate ≈ the grant gate, not a
   superset. **Community-tier plugins get NO Secret-read RBAC by default** (§7.3): the SecretBroker resolver is a
   trusted-tier capability. This restores the pod model's property that the executor can resolve *only* the
   specific Secret it was handed, structurally (RBAC), not by convention.

4. **Audit-of-record completeness (MF-D).** The core's **use-check audit is the single audit-of-record** (§1.6):
   there is NO resolution path without a preceding use-check audit entry. This falls out of MF-A — because the
   plugin's RBAC cannot exceed the granted set, every material resolution corresponds to a ref the core
   use-checked and audited, so the one-audit-stream invariant holds across the pod and plugin transports.

5. **Site-relay fail-closed is a BLOCKING PRECONDITION, not a follow-up (MF-C).** A relayed plugin at an
   untrusted Site learning hub Secret coordinates is a §2.5 breach, so this is a design precondition, not a
   Consequence. `ResolvedRef` coordinates are attached **only on the local/trusted execution path** and are
   **never serialized onto a relay transport** (ADR-0049) — a relayed/remote plugin gets the ref name alone and
   fails closed. And coordinate-withholding alone is **not sufficient**: the relay boundary must sit *above* the
   resolver, so a remote plugin has **no RBAC or network path to hub Secrets at all** (MF-A's per-plugin Secret
   confinement is what makes this enforceable — a Site plugin's RBAC never covers hub Secrets).

6. **notify/webhook over the port (the motivating extraction).** With the port in place, `notify/webhook`
   becomes a gRPC **plugin Action** (`stratt-notify`, trusted-tier per MF-A): its `Invoke` reads `args`
   (body/method/headers), resolves the Sink's `webhook` CredentialRef via the SDK SecretBroker to `{url, token}`,
   issues the one HTTP POST in-process, and streams the typed delivery result. The in-tree
   `core/internal/actuators/webhook` and `core/internal/actions/notify` are then deleted; the notifier's
   `RunAction("notify/webhook", …)` dispatch is unchanged (it routes by name to the plugin). The
   `actions/notify/webhook.*` and `contracts/actuators/webhook.input` Contracts stay (pinned schema data, §1.5) —
   the plugin validates against them.

**The pinned invariant (get this right at t=0):** *material is resolved at the executor, never in the spine.*
The core's job on the credential path is **use-check → audit → hand over coordinates**, identical for a pod
(`secretKeyRef` → kubelet) and a plugin (`ResolvedRef` → SDK SecretBroker). The port carries **coordinates, never
bytes**. Any design where the core reads a Secret to serve its material to a plugin is **rejected** — it would
put material in the spine, the one thing §2.5 forbids.

## Charter alignment

- **§2.5 — upheld, and deliberately *not* weakened.** The core continues to handle only names + coordinates;
  material resolution stays at the executor. The recommended shape was chosen *specifically* to preserve the
  "core never holds material even transiently" property that the pod path has today — see the rejected
  material-serving alternative below.
- **§1.5 (sovereign contracts, multiple transports):** the SecretBroker is a typed capability contract beneath
  which the backend (K8s Secrets today, Vault later) is a swappable transport — the §1.4 "boring spine,
  pluggable backend under measured pain" posture, applied to credential resolution.
- **§1.6 (one authz/audit):** the use-check chokepoint + per-ref audit are reused verbatim from the pod path —
  one credential authz model across pod and plugin transports (ADR-0051 MF7 generalized).
- **§1.4 / ADR-0046 — a stated *refinement*, with an asymmetry (F-1).** ADR-0046 named `SecretBroker` as a
  "sibling capability on the bus." This ADR **refines** it to an **SDK-side resolver over core-provided
  coordinates**, NOT a bus-served material RPC — a §2.5-*superior* realization (no material transits the spine),
  but a deliberate deviation to record: `SecretBroker` becomes **asymmetric** with `StateStore`/`EventBus`/
  `DurableExec`/`ArtifactStore`, which may still be bus-served. The core grows by a *mechanism* (coordinate
  hand-off), not a *domain*.
- **§2.5 / §3 literal-departure — the CONSCIOUS steward trade (F-3).** The §2.5 *property* (core never holds
  material) survives, but the design departs from §2.5's **literal** "injected only into execution pods at spawn"
  and §3's "K8s Jobs are the only execution primitive… ephemeral, secret-injected pods": material now lands in a
  **long-lived plugin process**, not a per-run ephemeral pod. The blast radius along the time + cross-principal
  axes is larger than the pod path, mitigated structurally by MF-A (RBAC ≈ grant) + MF-B (per-Invoke
  zeroize/no-cache/per-Principal). The **EE-Job Action alternative preserves the literal model** (pod-per-
  notification) and is the recorded fallback. **Acceptance of this ADR is the steward's conscious acceptance of
  the long-lived-executor trade** — it must not pass implicitly. (This is why the ADR is Proposed pending that
  sign-off, alongside the §2.5 review.)

## Consequences

- **Positive:** unblocks notify/webhook (and any per-call-credential plugin) over the port without a core
  material path; one credential authz/audit model across transports; the SecretBroker backend is swappable
  (Vault/KMS later) behind the SDK resolver; the core-size grows only by a typed coordinate field.
- **Negative / trade-offs:** a plugin now needs **Secret-read RBAC** in its namespace (the pod path needed none
  — the kubelet read on its behalf), a real broadening of the plugin's ambient authority that must be scoped
  tightly per-plugin and documented in the §7.3 trust-tier/sandbox posture. A long-lived plugin resolving
  material in-process holds it in memory for the call's duration, structurally bounded to per-Invoke by MF-B —
  the larger blast radius vs a per-run ephemeral pod is the F-3 trade the steward accepts on acceptance. The
  Site-relay coordinate-withholding is a **blocking precondition (MF-C)**, not a follow-up.
- **Follow-ups:** the Vault/KMS resolver backend; short-lived/rotating credential issuance; extending the §7.3
  sandbox posture to name plugin Secret-read RBAC explicitly. **A future "SecretBroker as a standalone brokering
  plugin" (a broker plugin serving OTHER plugins) is explicitly gated for its own §2.5 review (F-2)** — a broker
  concentrating material for peers reintroduces exactly the material-concentration blast radius the per-plugin
  SDK-resolver model avoids, and must not be built under this ADR's authority.

## Alternatives considered

- **A core-served SecretBroker RPC that returns material** (the literal "capability on the bus": the plugin calls
  `Resolve(ref)` and the core reads the Secret and streams back the bytes) — **rejected**: it puts credential
  **material in the spine**, destroying the "core never holds material even transiently" property §2.5 rests on.
  The whole point of the pod path's `secretKeyRef` is that the core hands a *reference* and the kubelet resolves;
  a material-returning broker throws that away.
- **Keep the pod + kubelet-mount model — extract notify as an EE-Job Action** (build "Invoke over the EE-Job
  transport") — **not chosen here** but a legitimate design: it preserves the kubelet model for free and needs no
  new credential path, at the cost of a pod-per-notification and a second Action transport. Recorded as the
  fallback if the SecretBroker's plugin-RBAC broadening proves unpalatable.
- **Static per-plugin credential (the awsec2 shape) for notify** — rejected: notify's credential is **per-Sink**,
  not per-plugin; a static process credential cannot serve many Sinks' url/tokens.
- **Pass material through the Envelope from the core** — rejected for the same reason as the core-served RPC:
  material would transit the spine.

## Reviews

- **charter-guardian, §2.5 design review (2026-07-17): SOUND-WITH-CHANGES → folded above.** The central seam is
  held: coordinates-only enrichment (no `bytes` field) + the explicit rejection of a material-returning core RPC
  is the correct, mandatory §2.5 call, structurally identical to `secretKeyRef → kubelet`. Four must-fixes,
  folded into the Decision: **MF-A** (a plugin's standing Secret-read RBAC must ≈ the per-call use-grant, not a
  superset — else the use-check is no longer the sole chokepoint; community-tier gets none), **MF-B** (structural
  per-Invoke ephemerality of in-process material — SDK mechanism, not guidance), **MF-C** (Site-relay fail-closed
  is a blocking precondition, and coordinate-withholding alone is insufficient — the relay must sit above the
  resolver so a remote plugin has no path to hub Secrets), **MF-D** (the core use-check audit is the single
  audit-of-record; no resolution without a preceding entry — falls out of MF-A). Flags resolved in-text: **F-1**
  (the ADR-0046 refinement + capability-class asymmetry, stated in Charter alignment), **F-2** (the future
  standalone brokering plugin gated for its own §2.5 review), **F-3** (the departure from the *literal* §2.5/§3
  pod-injection model to a long-lived executor — the property survives; acceptance of this ADR is the steward's
  conscious acceptance of that trade, with the EE-Job Action as the literal-model fallback).
