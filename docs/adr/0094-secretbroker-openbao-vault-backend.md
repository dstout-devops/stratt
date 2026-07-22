# ADR 0094 — SecretBroker OpenBao/Vault backend: per-call material from a KV store (§2.5)

- **Status:** **Accepted** (2026-07-22, steward) — charter-guardian **PASS-WITH-CHANGES** (§2.5
  review): core-never-holds-material and MF-A/C/D preserved; five points folded into this ADR + the
  implementation — (1) MF-B is preserved *by construction* (vault backend decodes KV values into
  `[]byte`, never intermediate `string`s, else un-zeroizable heap residue); (2) the fail-closed
  predicate generalizes to "no K8s **and** no vault coordinates"; (3) the pod-path vault rejection is
  a structural, named error, never a silent empty injection; (4) AppRole/K8s-auth elevated from
  follow-up to a **precondition for non-dev use**; (5) OpenBao-only for Stratt's own dev/CI, Vault
  (BUSL) is the operator's transport choice only. vocabulary-linter **PASS** (no renames).
- **Date:** 2026-07-22
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.5 (secrets brokered, never held by the core) · §1.5 (sovereign contracts,
  multiple transports) · §1.4 (boring spine — no new heavy dependency) · §1.6 (one Principal/authz/
  audit) · §1.7 (evergreen) · realizes the backend ADR-0052 **explicitly deferred**; builds on
  ADR-0009 (CredentialRef backends), ADR-0046 (SecretBroker capability class), ADR-0093 (the dev
  OpenBao backend).

## Context

`types.CredentialRef.Backend` has declared three backends since ADR-0009 — `k8s-secret`, `vault`,
`workload-identity` — but only `k8s-secret` resolves. ADR-0052 built the **plugin per-call
SecretBroker** (the core hands a plugin *coordinates*, never material; the plugin's SDK resolver reads
the material itself, use-checked + zeroized) and named a **Vault/cloud-KMS backend** as the first
follow-up it left **out of scope**. ADR-0093 just put a real `openbao` in the dev harness. This ADR
builds that backend: **a plugin can resolve a `backend: vault` CredentialRef to material from an
OpenBao/Vault KV store**, per call, without the core ever touching the material.

**The load-bearing constraint (unchanged from ADR-0052):** the core never holds credential material,
even transiently. For `k8s-secret` the kubelet-analogue is the plugin's own confined RBAC reading a
K8s Secret. For `vault` it is the plugin authenticating to OpenBao **as itself** (its own token/role,
a policy scoped to the granted paths) and reading the KV secret — the core only ever emits *vault
coordinates* (mount / path / key map) after the **use-check + audit** it already performs.

**A hard boundary this ADR draws:** `vault` resolves **only on the plugin SecretBroker path**
(ADR-0052). The **pod/EE-Job path cannot** — the kubelet resolves `secretKeyRef` against K8s Secrets,
not Vault; there is no vault material for it to inject. So a `vault` CredentialRef bound to an in-tree
pod Actuator (e.g. ansible) **fails closed, loudly** (§1.8), and pod-side Vault injection (a
vault-agent/CSI sidecar) is a named follow-up, not silent breakage.

**In scope:** the `vault` coordinate on the port (proto `ResolvedRef`), the core `ResolveCredentials`
`vault` case (coordinates only), the SDK resolver's OpenBao KV backend, and a live dev proof.
**Out of scope:** rotating/dynamic secrets (leases/renewal), the `transit` and PKI surfaces (Slices
E/F), the `workload-identity` backend, and pod-path Vault injection.

## Decision

Add **`vault`** as a second SecretBroker coordinate shape, resolved **plugin-side**, material-free
across the core — mirroring the ADR-0052 `k8s-secret` shape one-for-one.

1. **Port (proto, additive — §1.5).** `ResolvedRef` gains an optional `VaultCoords vault = 4`:
   ```proto
   message VaultCoords {
     string mount = 1;  // KV engine mount (e.g. "secret")
     string path  = 2;  // secret path under the mount
     bool   kv_v2 = 3;  // KV v2 (data wrapper) vs v1
   }
   ```
   Exactly one coordinate set is populated per `ResolvedRef`: either the existing
   `(secret_namespace, secret_name)` **or** `vault`. The shared `keys` list is unchanged — each
   `ResolvedKey.key` now names *the field within the KV secret* (as it already names the K8s Secret
   data key). **No material/`bytes` field is added — ever (§2.5).**

2. **Core `ResolveCredentials` (coordinates only, single audit — MF-D preserved).** The one
   use-check + audit stays exactly where it is; a new `case types.BackendVault` parses the vault
   `Locator` (`{"mount","path","kvV2"}`) into a `dispatch.CredentialMount{Vault: …}` carrying the
   coordinates. `workload-identity` remains `BackendUnimplemented`. **The pod dispatch path rejects a
   vault mount** — `secretKeyRef` cannot address Vault — with a named, diagnosable error, never a
   silent empty injection (§1.8). Vault mounts flow only to the plugin Invoke path.

3. **Envelope enrichment (`pluginhost.wireCred`, MF-C unchanged).** `pluginhost.Credential` gains
   `Vault *VaultCoords`; `wireCred` renders it into `ResolvedRef.vault` **only when the host allows
   coordinates** (the local/trusted path) — a relay host still withholds every coordinate, so hub
   Vault paths never cross an untrusted Site (fail-closed).

4. **SDK resolver (`sdk/secretbroker`) — one entrypoint, two backends, ephemerality shared.**
   `WithMaterial` dispatches by coordinate kind: `ResolvedRef.vault` → the OpenBao KV backend;
   otherwise → the existing K8s backend. The **shared** `WithMaterial` keeps MF-B (zeroize before
   return) and MF-C (no coordinates ⇒ fail closed) for *both* backends — the vault backend only reads
   raw field bytes; it never owns the use closure or the zeroize. Two structural requirements make
   this honest:
   - **Fail-closed predicate (MF-C).** The withheld-coordinates guard must be "**no K8s coordinates
     AND no vault coordinates**" — a valid `vault` ref legitimately carries an empty `secret_name`, so
     the old `secret_name == ""` test would misclassify it as withheld. A relay-stripped ref (neither
     set) still fails closed.
   - **Zeroizable material (MF-B).** The vault backend must decode KV field values **directly into
     `[]byte`** (via `json.RawMessage`, never an intermediate Go `string`), so the shared
     `defer m.zero()` can actually wipe them. A JSON decode into `string` would leave an
     **un-zeroizable heap residue** the K8s path does not have — so MF-B is *preserved by
     construction*, not for free (see Charter alignment).

   The OpenBao backend is a **tiny `net/http` KV client** (the `plugins/certissuer` precedent — **no
   new Go dependency**, §1.4), configured from the **plugin's own** env
   (`STRATT_SECRETBROKER_VAULT_ADDR` + a token/role) — the plugin authenticates to OpenBao **as
   itself**, under a policy scoped to the granted paths (MF-A). A `vault` coordinate arriving at a
   plugin with **no** Vault client configured **fails closed**.

5. **Dev proof (ADR-0093 harness).** `openbao-bootstrap.sh` enables a KV v2 engine and seeds one demo
   secret; an estate `CredentialRef{backend: vault}` + a use grant lets a plugin resolve it live,
   use-checked and zeroized. `task ci` green.

## Charter alignment

- **§2.5 (secrets brokered, never held by the core) — the whole point.** The core emits *vault
  coordinates* after a use-check + audit; the plugin resolves material itself, as itself, confined to
  the granted paths. No material field exists on the wire. MF-A (confined identity), MF-C (relay
  fail-closed), and MF-D (single use-check/audit) are preserved unchanged. **MF-B (per-Invoke
  zeroize) is preserved *by construction, not for free*:** an HTTP/JSON KV read would leave
  secret values as immutable Go `string`s that no `zero()` can wipe (they linger on the heap until
  GC) — the K8s path never has this because it copies `[]byte` out of the Secret. Decision §4
  therefore *requires* the vault backend to decode into `[]byte` so the shared zeroize is real. This
  residual-heap risk is disclosed, not hidden (§1.8).
- **§1.5 (sovereign contracts, multiple transports).** `vault` is another **transport** beneath the
  same SecretBroker contract; the port stays the authority. Additive proto — no breaking change.
- **§1.4 (boring spine).** No new dependency — a ~one-file `net/http` KV client, matching certissuer.
- **§1.3 (rug-pull-proof).** OpenBao (MPL-2.0) is the rug-pull-safe transport — the whole reason it,
  not Vault, is in the harness (ADR-0093). The port speaks the Vault-compatible KV HTTP API, so an
  operator *may* point a plugin at HashiCorp Vault (BUSL-1.1) — but that is **the operator's transport
  choice, never a Stratt dependency**. Stratt's own dev and CI surface stays **OpenBao-only**; the
  BUSL fork can never become load-bearing.
- **§1.6 (one Principal/authz — MF-A tension, flagged F-2).** The plugin's Vault identity is a
  *second* operator-configured trust surface beside its K8s RBAC. A **static env token** is a standing
  credential broader than any single launching Principal's use-grant (un-attributable per-Principal at
  the Vault layer — the same shape as a shared K8s ServiceAccount, so no regression, but the surface
  doubles). Mitigation is a **precondition for any non-dev use**, not a nicety: Vault **AppRole or
  Kubernetes auth** scoped to the granted paths, with the plugin's own login credential itself
  brokered as a bootstrap `k8s-secret` CredentialRef — never a long-lived root token. Dev mode uses
  the in-memory root token (ADR-0093) and is explicitly not a production posture.
- **§1.7 (evergreen).** OpenBao is already a blessed dev dependency (ADR-0093); no version surface
  added.
- **Tension noted (F-1):** `vault` deliberately resolves on the **plugin path only**; the pod path
  fails closed. This is a *narrower* capability than the three-backend `CredentialRef` type implies at
  face value. Accepted as an honest partial (§1.8: a diagnosable refusal, not a silent gap) with
  pod-side injection named as a follow-up — not hidden.

## Consequences

- **Positive:** the first non-K8s SecretBroker backend; unblocks credentialed plugin testing against a
  real, rug-pull-safe KV store; proves the ADR-0052 port generalizes exactly as designed (a second
  backend with **zero** change to the MF-A..D invariants).
- **Negative / trade-offs:** `vault` is plugin-path-only until a pod-side vault-agent lands; the SDK
  resolver now needs Vault client config wired into any plugin that opts in (env, MF-A policy).
- **Follow-ups:** pod-path Vault injection (vault-agent/CSI); Vault **AppRole/K8s-auth** login instead
  of a static token for the plugin identity; dynamic/leased secrets; fold the KV *metadata* Syncer
  (Slice G) and Transit (Slice F) onto the same `openbao` surface.

## Alternatives considered

- **Resolve Vault material in the core, inject like today.** Rejected — the core would hold material,
  the exact §2.5 violation ADR-0052 was built to avoid.
- **`oneof` wrapping the existing k8s fields.** Rejected — breaks the shipped `GetSecretName()` /
  `GetSecretNamespace()` getters (host + SDK) for no invariant gain; an additive `vault` field with a
  documented "exactly one populated" rule is backward-compatible and evergreen-friendlier.
- **Vendor the official `openbao/api` (or `hashicorp/vault/api`) Go client.** Rejected for the SDK —
  a heavy transitive tree for a single KV read; the certissuer `net/http` precedent is the §1.4
  boring-spine choice. (Revisit if AppRole/dynamic-secret surface area grows.)
- **Add pod-path Vault injection now.** Deferred — a vault-agent/CSI sidecar is its own decision;
  bundling it would balloon the slice and couple two independent seams.
