# ADR 0098 — Consolidate the OpenBao surfaces into `plugins/openbao`; full-featured PKI behind the neutral cert-issuer Contract

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian **PASS-WITH-CHANGES** (the §1.5
  plugin/Contract split is textbook; `cert-issuer` reconciles the identifier back to the neutral name
  ADR-0030 always intended — a clarification, not a §2 break, and the repo is still private so no frozen
  external consumer). Findings folded: (1) **`put-role` DROPPED** — a named issuing role is declarative
  config, which belongs on the reconcile/Git-declared path, not a fire-and-forget Action (the
  "parked-on-Actions" shape §2.3 forbids); roles stay OpenBao-administered (bootstrap-seeded), with
  reconcile-to-declared-role a named follow-up. (2) **`create-intermediate` fails closed on an existing
  issuing CA** (converge-to-one, never double-mint-reported-green, §1.8). (3) **Never-implement-a-CA
  invariant:** every PKI Action is a thin OpenBao `/pki` HTTP call — Stratt integrates the CLM, never
  generates/signs in-process (ADR-0030 non-goal). (4) The Action-execution framework now exists
  (`core/internal/actions/action.go`), legitimizing E2 hosting real §2.2 Actions. vocabulary-linter
  **PASS**. **Phased: E1 = the rename (behavior-preserving); E2 = the PKI build.**
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts, multiple transports — the Contract is neutral, the
  plugin is a transport) · §2 (vocabulary — the Actuator/Contract name is frozen-API-shaped) · §1.4
  (boring spine) · §1.1 (type the seams) · §1.2 (projections) · revisits the naming of ADR-0030 (CLM
  connector) / ADR-0050 (cert reconcile Actuator); prepares the home for Slice F (Transit) and Slice G
  (KV Syncer).

## Context

The OpenBao-backed surfaces are scattered under the vague name **`certissuer`**: the plugin module
(`plugins/certissuer`), the plugin identity, **and** the Actuator/Contract name are all "certissuer,"
conflating two different things — *the tool* (OpenBao) and *the role* (a certificate issuer). Meanwhile
future OpenBao surfaces (KV metadata Syncer — Slice G; Transit-adjacent — Slice F) have no clear home.

The charter itself already uses the **neutral** term "**CLM connector**" (§: "issuer ref to CLM
connector"), never "certissuer" and never "OpenBao." ADR-0030/0050 kept the *Contract* issuer-agnostic
(any PKI-compatible CLM) while the plugin happens to speak OpenBao's `/pki` API. The fix is to make the
naming honest on both axes:

- **The plugin is tool-named `openbao`** — like `vcenter`/`awsec2`/`awss3`, a plugin may name its
  backend; it is a *transport* beneath the sovereign Contract (§1.5). This is where every OpenBao
  surface consolidates (PKI now; KV Syncer + Transit later).
- **The Contract/Actuator is neutral `cert-issuer`** — the frozen-API name Steps bind to (a step-ca or
  AWS-PCA plugin could implement the same Contract). "cert-issuer" is clearer than the vague
  "certissuer" while staying issuer-agnostic (never "openbao-by-name" in the Contract).

**In scope:** the consolidation rename **(E1)** + a full-featured PKI build **(E2)**.

**Out of scope:** Slice F (Transit — a core seam, separate ADR) and Slice G (KV Syncer — folds into
`plugins/openbao` under its own slice); migrating any *other* plugin.

## Decision

### E1 — Consolidation rename (behavior-preserving)

1. **Plugin** `plugins/certissuer` → **`plugins/openbao`** (module, Go package `openbao`, `cmd/
   stratt-plugin-openbao`, Dockerfile, `go.work` entry). PluginIdentity + `Source.Kind`: `certissuer`
   → **`openbao`**.
2. **Actuator/Contract** `certissuer` → **`cert-issuer`** (neutral): the reconcile Actuator name Steps
   bind to; `contracts/actuators/certissuer.input.schema.json` → `cert-issuer.input.schema.json`
   (`$id` updated); the plugin's manifest + strattd `registerPluginActuator("cert-issuer", …)`.
3. **Env** `STRATT_CLM_*` → **`STRATT_OPENBAO_*`** (`_PLUGIN_ADDR`, `_ADDR`, `_TOKEN`, `_ROLE`,
   `_SOURCE_NAME`, `_INTERVAL`) across the plugin `main.go`, strattd, `openbao-bootstrap.sh`,
   docker-compose, chart values.
4. **Vestigial cleanup:** the retired imperative cert Actions (`contracts/actions/certissuer/{issue,
   renew,revoke}.{input,output}`) — superseded by the reconcile Actuator (ADR-0050 §Consequences,
   "retires that dangling Action-framework commitment") — are **removed** along with their tests. Pin
   count drops accordingly.
5. **Estate + docs:** the estate bindings (`actuator: certissuer` → `cert-issuer` in blueprints/
   triggers/views/workflows) and the roadmap/architecture references are updated. The **charter is NOT
   edited** (it already says "CLM connector," which remains correct).

### E2 — Full-featured PKI (behind the neutral Contract)

The cert *lifecycle* stays the reconcile Actuator (Sign/Revoke/Observe, born-on-target CSR — ADR-0050,
unchanged). E2 adds the **administrative** PKI surface (legitimately imperative Actions — these are CA
setup, NOT the retired per-cert issue/renew/revoke) + richer observation:

1. **Intermediate CA** — `cert-issuer/create-intermediate`: a thin OpenBao `/pki` call (generate CSR →
   sign under the root/operator parent → set-signed) — **never** in-process key generation/signing
   (never-implement-a-CA). **Fails closed** if an issuing CA already exists (converge-to-one; a
   re-invoke does not mint a second intermediate and report green — §1.8). Output `{caSerial}`.
2. **Issuing roles** — NOT an Action (guardian §2.3): a named role is declarative config, so it stays
   OpenBao-administered (seeded by `openbao-bootstrap.sh`); reconcile-to-declared-role is a follow-up.
3. **CA-hierarchy observation** — the Syncer projects the mount's issuing CA as a **`ca`** Entity kind
   (identity `pki.caSerial`, closed Facet `ca.config {commonName, notAfter, isCA}`). `cert-issuer/
   rotate-crl` rotates the CRL (thin admin call).
4. All new Actions are gated by CredentialRef use-check (§2.5); the plugin talks OpenBao with its own
   token (`STRATT_OPENBAO_TOKEN`), never through the core.

**Deliberately NOT in E2 (conflicts surfaced during implementation):**
- **`cert.revocation` Facet — dropped.** Revocation is already observed by the ADR-0050 **tombstone**
  model: a revoked cert is *absent* on the next full-sync and the host tombstones it — that absence
  IS the revocation signal. Retaining revoked certs with a `{revoked}` Facet would *contradict* the
  Destroy-tombstones-cert model; the audit trail lives in Run history + Findings/Evidence (§7), not a
  retained graph Entity.
- **`signed-by` Relation + intermediate observation — deferred.** An intermediate lives in a *separate*
  mount (`pki_int`) the single-mount Syncer does not enumerate, and a `pki.caSerial` Relation needs the
  parent's serial the child cert does not carry. Multi-mount CA-graph observation (root→intermediate
  `signed-by`) is a named follow-up; `create-intermediate` returns the `caSerial` as bindable output
  meanwhile.

## Charter alignment

- **§1.5 (sovereign contracts, multiple transports).** The Contract (`cert-issuer`) is the authority
  and stays issuer-agnostic; `openbao` is one transport beneath it. Renaming the plugin to its backend
  is honest and matches `vcenter`/`awsec2`/`awss3`; the neutral Contract name is preserved (the
  ADR-0030 "never-openbao-by-name-in-the-Contract" invariant).
- **§2 (vocabulary).** `cert-issuer` / `ca` are neutral, cloud/PKI-native, not banned terms, not new
  Named Kinds. The rename is a *clarification within* the frozen naming (certissuer was never a Named
  Kind; it was an Actuator instance name), so it is API-shaped but not a §2 v1.0 break — the Actuator
  name is an instance identifier, not a Named Kind. vocabulary-linter gate.
- **§1.1 (type the seams).** New closed Facets (`cert.revocation`, `ca.config`) demanded by the
  shipping Syncer; co-fidelity tested.
- **§1.2.** CA/CRL/revocation are *observed* projections (Source provenance); the reconcile writes cert
  Entities with Run provenance. No second writer.
- **§1.4.** No new dependency — the hand-rolled `net/http` OpenBao client is extended.

## Consequences

- **Positive:** honest naming (tool-named plugin, neutral Contract); one home for all OpenBao surfaces
  (unblocks F/G cleanly); a genuinely full-featured PKI (intermediate CA, roles, CRL, revocation
  observation) rather than sign/revoke only; the vague `certissuer` name retired.
- **Negative / trade-offs:** a wide behavior-preserving rename (≈40 files incl. estate + generated-code
  comment examples + tests) — the risk is a missed reference, mitigated by keeping the tree green at
  each phase; the `actuator: certissuer` estate binding is a breaking change (updated in-repo; any
  external estate must rename the binding).
- **Follow-ups:** Slice F (Transit) + Slice G (KV Syncer) land in `plugins/openbao`; the born-on-target
  CSR integration contract (ADR-0050 §3) is unchanged.

## Alternatives considered

- **Plugin rename only, keep the `certissuer` Actuator name.** Rejected — leaves the vague name on the
  load-bearing Contract binding, only half-addressing the concern.
- **Name the Contract `openbao`.** Rejected — violates ADR-0030's issuer-agnostic Contract (the
  Contract must not be openbao-by-name); a step-ca plugin must be able to implement `cert-issuer`.
- **Reintroduce imperative issue/renew/revoke Actions for the full-featured PKI.** Rejected for cert
  *lifecycle* (ADR-0050 retired it for principled reconcile reasons); the new Actions are *CA/role
  administration*, a distinct concern that is legitimately imperative.
