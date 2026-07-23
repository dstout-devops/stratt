# ADR 0106 — OpenBao as a multi-capability provider; enablement-gate vs resolve-inject capabilities

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian PASS (D1 reach-path guardrail + F1/F2 folded), vocabulary-linter CLEARED.
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts — a capability is a contract; OpenBao is one provider of
  each, swappable) · §1.4 (boring spine, pluggable everything — OpenBao is community breadth behind core
  contracts) · §1.1 (type the seams, build only what a Contract demands — declare `provides` on the
  *existing* provider, don't build a speculative consumer) · §2.5 (secrets brokered, never baked — the
  secretbroker material path stays SDK-side, coordinates-only over the port) · §1.8 (never hide diagnosis
  — an unverified provider is queryable). Builds on ADR-0104 (capability dependencies + provider
  verification), ADR-0105 (the resolve-inject pattern + class-level Contract), ADR-0100 (KeyCustodian —
  WrapKey/UnwrapKey + portCustodian), ADR-0094 (SecretBroker vault backend — coordinate handback),
  ADR-0098 (OpenBao PKI / the `cert-issuer` Actuator), ADR-0099 (KV metadata Syncer).

## Context

statestore (ADR-0105) proved the framework with a **resolve-inject** capability: the core invokes a
resolve Action, validates a class Contract, and injects a handle. OpenBao is the next provider — and it
reveals that **not every capability is resolve-inject.** The `plugins/openbao` binary already implements,
end-to-end, three OpenBao surfaces, each consumed by a *different* mechanism that predates the framework:

- **keycustodian** — dedicated `WrapKey`/`UnwrapKey` port verbs, **core-consumed** via `portCustodian`
  (ADR-0100). Per ADR-0104 D7 the core's own use deliberately stays there.
- **certissuer** — a **target-scoped Apply**: a scheduled Trigger → the `cert-issuer` Actuator signs a
  born-on-target CSR (ADR-0098). Issuance is an Apply against targets, not a config handle.
- **secretbroker** — **coordinate handback**: the core hands Vault *coordinates*, the plugin **SDK**
  resolves material with its own token, zeroized per call (ADR-0094/0052 MF-A/B). Material resolution is
  structurally SDK-side, never the openbao plugin's job (its KV touch is metadata-only, ADR-0099).

None fits statestore's resolve-inject shape. And the manifest advertises only `keycustodian` — a
**dangling token** (no consumer, no resolve Action). No declaration anywhere `requires` any capability
yet, so the framework's consumer side is exercised only in tests.

## Decision

### D1 — Two capability consumption shapes; the framework supports both

A capability class is a contract (§1.5), but *how a consumer obtains it* has two shapes:

- **Resolve-inject** (ADR-0105): the core invokes a `<provider>/<class>-resolve` Action, validates the
  class-level output Contract, and injects a `CapabilityHandle` onto the consumer's Apply/Plan. For
  low-rate *config/coordinate* resolution the consumer then renders (statestore → tofu `-backend-config`).
- **Enablement-gate** (ADR-0104): the capability is consumed via its **own** port/path (dedicated verbs,
  a CredentialRef coordinate, a class-pinned Actuator shape). `requires: [X]` gates only that a **verified
  provider of X exists** before the dependent enables — **no handle is injected**. The consumer reaches
  the provider through that path.

  **Guardrail (§1.5 — the reach-path must be the CLASS's contract, never a provider's):** "its own path"
  means the **capability class's** sovereign, provider-agnostic contract — dedicated port verbs
  (keycustodian's `WrapKey`/`UnwrapKey`) or a **class-pinned Actuator shape** — with provider *selection*
  via an estate binding (ADR-0105 D5), **never** a named provider's specific mechanism. Otherwise a second
  provider (aws-kms for keycustodian; step-ca/ACME for certissuer) would force the consumer to change its
  reach-path, silently reintroducing the §1.5 provider-coupling the whole framework forbids. Enablement-gate
  is *"swap provider, zero consumer change"* exactly as much as resolve-inject — the gate must not become a
  loophole. keycustodian satisfies this (WrapKey/UnwrapKey is the class contract, portCustodian selects the
  provider); certissuer's consumer is deferred (follow-up #1) and is **bound by this guardrail**.

This distinction is the reusable output of this ADR: a new capability picks a shape, and the framework
(verification + `classifyRequires` for the gate; the resolve-Action path only for resolve-inject) already
supports it. **Both OpenBao capabilities are enablement-gate**, so **neither gets a resolve Action.**

### D2 — OpenBao is a verified multi-capability provider of `keycustodian` + `certissuer`

The openbao plugin genuinely *implements* two capability classes, so it advertises them honestly and is
declared a registry provider:

- **`keycustodian`** — WrapKey/UnwrapKey (transit.go). Already advertised; kept, and **unconditionally**
  (not mount-guarded like certissuer): OpenBao Transit needs only the `Addr`+`Token` the plugin must have
  to run at all — it ensures per-domain keys itself, with no separate mount to gate on — so unconditional
  advertisement is *honest* (a running plugin can always Wrap/Unwrap), not the asymmetry it appears
  (guardian F2). certissuer differs because a PKI mount genuinely may be absent.
- **`certissuer`** — the capability class for PKI issuance, consumed via the neutral `cert-issuer`
  reconcile Actuator (sign/revoke born-on-target CSRs via OpenBao `/pki`, ADR-0098). Newly advertised in
  the Manifest, **guarded on the PKI mount config** (like awss3 guards `statestore` on a state bucket) so
  provider verification (ADR-0104 D1) stays honest — the plugin advertises certissuer only where it can
  back it.

A registry Actuator declaration `openbao` (`estate/actuators/openbao.yaml`) declares
`provides: [keycustodian, certissuer]`, dials the openbao pod, and is verified. It carries **no resolve
Action** (enablement-gate, D1); it exists to advertise + be verified, reconciling the dangling token.
This is honest advertisement of *existing, working* capability — **not** speculative machinery (unlike
ADR-0105's deferred artifactstore provider), so §1.1 is satisfied even before a `requires` consumer lands.

### D3 — `secretbroker` is NOT a plugin `provides` — it is SDK-side by construction (§2.5)

The secretbroker *class* exists, but its "provider" is not the openbao plugin: material resolution is the
consuming pod's **SDK SecretBroker** reaching OpenBao KV with its own token (ADR-0094 MF-A/B). The core
never touches material; it hands a Vault **coordinate** on the CredentialRef path. So `secretbroker` is
**not** advertised by the openbao plugin and **not** in its `provides` — declaring it there would be
dishonest (the plugin does not resolve material) and would invite a resolve-Action that duplicates the
CredentialRef backend switch. The capability is real and already served; it simply has a different
provider locus (the SDK), out of scope for a registry plugin `provides`. Formalizing "SDK-provided
capabilities" is a booked follow-up if a `requires: [secretbroker]` gate is ever demanded.

**This supersedes ADR-0104's illustrative "What this looks like" table** (guardian F1), which listed
OpenBao advertising `secretbroker` under `Manifest.capabilities`. That was a sketch before the SDK-vs-plugin
provider locus was pinned down; D3 corrects it — OpenBao does **not** advertise `secretbroker`. The two
ADRs must not silently disagree.

### D4 — keycustodian's core consumption stays on `portCustodian` (ADR-0104 D7)

Declaring `provides: [keycustodian]` on the registry is the **plugin→plugin advertisement** (so a future
*plugin* consumer could `requires: [keycustodian]` and be gated on a verified provider). The **core's own**
envelope-encryption consumption is unchanged — it stays on ADR-0100's `portCustodian` + `localCustodian`
floor, selected by `STRATT_KEYCUSTODIAN_PROVIDER`, over the dedicated WrapKey/UnwrapKey verbs. The registry
`provides` and the core custodian are two non-conflicting facts about the same plugin.

### D5 — Consumers are booked, not built (§1.1)

No declaration `requires` keycustodian or certissuer today. This ADR ships the **provider** side (advertise
+ declare + verify), which is non-speculative (D2). The first consumers are booked:
- **certissuer**: a cert-needing **Blueprint/Workflow Step** that `requires: [certissuer]` — the "next
  lifecycle slice" the estate already references. As an enablement-gate, it gates the Step on a verified
  cert-issuer, then issues via the existing `cert-issuer` Actuator path.
- **keycustodian**: a *plugin* that needs envelope encryption of its own artifacts could `requires:
  [keycustodian]` — none exists yet; the core path stays portCustodian (D4).

## Consequences

- **Positive.** OpenBao becomes the ADR-0104 D1 **multi-capability provider exemplar**, verified, with its
  dangling token reconciled. The enablement-gate vs resolve-inject distinction (D1) is the reusable design
  output — every future capability now has a clear shape to pick, and the framework already supports both.
  Zero new port surface (no resolve Action for enablement-gate); the change is a manifest advertisement + an
  estate declaration.
- **Negative / cost.** A capability class can now be either shape, so a consumer author must know which — a
  documentation/vocabulary burden mitigated by D1 being explicit and by each class's ADR stating its shape.
  `secretbroker` sits outside the registry-`provides` model (D3), a slight asymmetry that is honest rather
  than forced.
- **Scope discipline.** Ships: keycustodian/certissuer advertised + verified via an `openbao` provider
  declaration. Defers: any `requires` consumer (D5); a "SDK-provided capability" model for secretbroker
  (D3); migrating the full openbao boot-env (Syncer + cert-issuer Actuator) onto the registry — the
  provider declaration coexists with the boot-env wiring, as `s3-statestore` does with awss3.

## Alternatives considered (rejected)

- **Give certissuer/secretbroker resolve Actions (mirror statestore).** Rejected (D1/D3): cert issuance is
  a target-scoped Apply and secretbroker is a per-call material path — a resolve Action would either not
  fit (certs aren't a config handle) or duplicate the CredentialRef switch. Enablement-gate is the honest
  shape.
- **Declare `provides: [secretbroker]` on the openbao plugin.** Rejected (D3, §2.5): the plugin does not
  resolve material; that is SDK-side. Advertising it would be a dishonest manifest and fail the spirit of
  provider verification.
- **Build a cert consumer now to avoid provider-with-no-consumer.** Rejected (D5, §1.1): declaring
  `provides` on an *existing* provider is honest advertisement, not speculative machinery — the discipline
  targets building unused *machinery*, which this doesn't. The consumer lands when demanded.
- **Fold keycustodian's core use into the registry framework.** Rejected (D4, ADR-0104 D7): the core custodian
  has a local floor and dedicated verbs; re-homing it buys nothing and risks the deterministic core.

## Follow-ups (separate slices / ADRs)

1. The first `requires: [certissuer]` consumer — a cert Blueprint/Workflow Step (the booked next lifecycle
   slice); designs how an enablement-gate consumer *reaches* its provider once gated, **bound by the D1
   guardrail**: the reach-path must be the `certissuer` CLASS's provider-agnostic contract (a class-pinned
   signing Actuator shape + an estate binding for provider selection), so step-ca / ACME are drop-in — never
   OpenBao's specific `cert-issuer` Actuator. That is the point at which the certissuer contract shape gets
   pinned; this ADR must not let the deferral become provider coupling.
2. A "SDK-provided capability" model for `secretbroker` (D3), only if a `requires: [secretbroker]` gate is
   demanded.
3. Guard keycustodian advertisement on transit availability for full manifest honesty (currently
   unconditional; the WrapKey impl is present but a missing transit token would fail at use-time).
