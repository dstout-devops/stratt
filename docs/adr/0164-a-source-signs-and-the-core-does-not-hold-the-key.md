# ADR 0164 — A source signs, and the core does not hold the key

- **Status:** **Accepted** (2026-08-03, steward) — **live-proven**: a signed report is verified
  against a key held in OpenBao as `exportable: false`, which the control plane cannot read; one
  changed byte is refused. Gated by `demo:network-device` and therefore by E2E-1. See Verification. Charter review by hand — this session's rules bar
  the subagent; §1/§1.4/§1.8/§2.5/§2 (vocabulary) answered inline. **No new runtime dependency**;
  one new capability class on the existing port.
- **Date:** 2026-08-03
- **Deciders:** steward
- **Charter sections:** §1 (no new configuration languages), §1.4 (boring spine), §1.8 (never hide
  diagnosis), §2.5 (credentials brokered — the core never holds material), §2 (vocabulary is API)
- **Pays ADR-0163 D4** verbatim: *"a new webhook source needs no core change — for its BODY. Its
  AUTHENTICATION is still core-held and single-shaped, and that is a real remaining gap."*
- **Builds on ADR-0052** (SecretBroker — the invariant this ADR is shaped by) and **ADR-0100**
  (KeyCustodian — the precedent for a core-consumed capability that keeps material in the plugin).

## Context

ADR-0163 made a webhook source's payload SHAPE a declaration, so a source core has never heard of
arrives without core changing. Its authentication did not move: ingest accepts exactly one thing, a
shared token in `X-Stratt-Emitter-Token`, compared against `hex(sha256(token))`.

Reading what real sources actually send splits the gap in two, and the halves are not the same
problem.

### Half one: the token is fine, the header is not

GitLab sends `X-Gitlab-Token: <secret>`. Grafana, Netbox and a long tail of others do the same thing
in their own header name. **That is already our model** — a shared secret presented in full, checked
against a stored hash — and the only reason it does not work is that core insists on the header name.

This half needs a declared string. It touches no invariant.

### Half two: the source signs the body, and that is a §2.5 problem

GitHub sends `X-Hub-Signature-256: sha256=<hmac>`; Stripe and Slack do equivalent things. Verifying an
HMAC requires **the shared secret itself**, not a hash of it — and ADR-0052 states, as a property it
explicitly declined to weaken:

> **the core never holds credential material, even transiently.**

So the obvious implementation — the Emitter references a secret, core reads it and computes the MAC
— is not a shortcut to be weighed against a purist objection. It contradicts an Accepted decision at
its central point, and would do so in the daemon that terminates untrusted inbound HTTP, which is the
worst possible place to start keeping shared secrets at rest.

**Note what the invariant does NOT forbid**, because the distinction is what makes half one legal:
core already handles what a *caller presents* — the SCIM and Emitter paths both hash a presented
bearer token in memory and compare. Seeing what arrives at the door is unavoidable for any inbound
authentication. Holding the key **at rest** is the line.

### What this must reconcile with

| Machinery                                                                       | Bearing                                                                              |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| ADR-0052 — core never holds credential material, even transiently               | The constraint that decides the whole design                                          |
| ADR-0100 — KeyCustodian: core CONSUMES a capability; the KEK never leaves the KMS | The precedent — a cryptographic operation delegated to the key's holder               |
| ADR-0104 — plugin capability dependencies                                       | How a new capability class is declared and resolved, rather than hardcoded            |
| ADR-0163 — the ingest path stays synchronous and plugin-free                     | Deliberately revisited here, and the asymmetry is argued rather than assumed          |
| ADR-0035 / ADR-0018 — SCIM and Emitter bearer tokens hold only `sha256`          | The hash model half one keeps, unchanged                                              |
| `plugins/openbao` already implements the custodian; Transit does HMAC natively   | The first provider is a plugin that already exists, not one this ADR invents          |

## Decision

### D1 — The header a source presents its token in is DECLARED

```yaml
name: gitlab-pushes
kind: webhook
tokenHash: <hex(sha256(token))>
token:
  header: X-Gitlab-Token # default: X-Stratt-Emitter-Token
  prefix: "" # e.g. "Bearer " — stripped before comparison
```

Core still stores only the hash, and the comparison is still constant-time. **Nothing about the trust
model changes**; a string that was hardcoded became a declaration, which is the same move ADR-0163
made one layer down.

### D2 — Signature verification is DELEGATED to the key's holder; core stays hash-only

A new capability class, `macverifier`, on the existing port:

```proto
rpc VerifyMAC(VerifyMACRequest) returns (VerifyMACResponse);
```

Core sends the **raw body**, the **presented signature**, and the **key coordinates** the Emitter
declares. The plugin — OpenBao Transit first, because it already holds keys for this repo and does
HMAC natively — computes and compares, and answers a boolean. **The key never leaves the KMS**, which
is ADR-0100's sentence with one noun changed.

```yaml
verify:
  header: X-Hub-Signature-256
  algorithm: hmac-sha256
  encoding: hex # hex | base64
  prefix: "sha256=" # stripped before decoding
  keyRef: gh-webhook # coordinates, never material (§2.5)
```

**Everything in that block is data**, and the reason matters: a signature scheme is a small closed set
of choices (which header, which hash, which encoding, what prefix), so it is describable without an
expression language (§1). What it is NOT is a place to put a template.

### D3 — Verification happens on the RAW BYTES, before anything parses them

This is the implementation hazard that makes or breaks the feature, so it is a decision rather than a
note. A MAC covers the exact bytes the source sent. Ingest must:

1. read the body,
2. **verify**, on those bytes,
3. only then parse and explode (ADR-0163).

Re-serializing a parsed payload and verifying that would fail on whitespace, key order, and number
formatting — and would fail *intermittently*, which is worse than failing. Nothing between the socket
and the verifier may normalize the body, and the guard for this is a test with a body whose
re-serialization is provably different from its original bytes.

### D4 — A plugin on the ingest path is accepted HERE, and ADR-0163's argument is why it was not there

ADR-0163 refused to route the fan-out through a plugin: ingest answers a live request from a system
that drops the alert if we do not take it, so a plugin round-trip put a third party on the path of the
one surface whose reliability is the product.

That argument does not transfer, and the difference is not convenience:

- **For shaping there was a cheaper correct option** — core can walk a declared path itself, holding
  nothing it should not. The plugin bought nothing and cost availability.
- **For verification there is no such option.** Checking a signature requires the key. Core cannot do
  it without breaking ADR-0052, so the choice is not "plugin hop vs. core does it cheaply" — it is
  "plugin hop vs. the capability does not exist."

The availability cost is real and stated rather than argued away: **if the verifier is unreachable, a
signed source's events are refused**, and the estate must see that as a refusal (§1.8) rather than as
silence. An Emitter that declares no `verify` is untouched — the hop exists only where a signature
does.

### D4b — This is a BOUNDARY CHANGE, and it is declared as one

ADR-0137 D2 says developing a plugin changes nothing outside `plugins/<name>/`, and
`task plugins:boundary:diff` enforces it against the diff. This change touches `plugins/openbao`
**and** core, so it trips that gate — correctly.

It is the legitimate case the gate's escape hatch exists for, and that hatch is a STATEMENT rather
than a switch: a `Boundary-Change:` trailer that puts the crossing in front of a reviewer. A new
capability class cannot live inside one plugin — `VerifyMAC` is a port verb (`proto/`), a core-side
consumer (`core/internal/macverify`) and a first provider (`plugins/openbao`), the same three-part
shape ADR-0100 has for KeyCustodian. What core learns is a capability CLASS; it never learns that
openbao exists.

**It also caught something worth keeping.** The gate is diff-based against `origin/main`, and a
local `task ci` SKIPS it when `BASE` is unset — so this passed on the workstation and failed in CI.
Running it as CI does is `BASE=origin/main task plugins:boundary:diff`, and any change spanning a
plugin and core should.

### D5 — What is declined, each for a stated reason

- **Core resolves the secret and HMACs it itself.** Refused: it contradicts ADR-0052's central
  property, in the daemon terminating untrusted inbound HTTP. If it is ever wanted, it is an
  amendment to ADR-0052 at that ADR's review bar, not a quiet exception here.
- **Verify at an ingress/gateway.** Declined. It works, and it makes the estate's declaration untrue:
  the Emitter would claim an authentication the platform does not perform, "why was this rejected?"
  would leave Stratt entirely (§1.8), and every adopter would rebuild it.
- **Timestamped anti-replay schemes** (Stripe's `t=...,v1=...`, which signs `timestamp.body` and
  demands a tolerance window). **Booked, not built.** It is a different shape — a second field inside
  the header, a clock comparison, and a tolerance nobody can pick for you — and folding it in would
  make this ADR's testable claim mushy. What ships covers GitHub, GitLab, Slack-style raw-body MACs.

## Consequences

- **A signed source becomes reachable at all**, which is the gap ADR-0163 left named and open.
- **One new capability class** (`macverifier`) and one new RPC. Additive; a provider that cannot HMAC
  simply does not advertise it, and an estate that declares no `verify` never resolves one.
- **A new failure mode on the ingest path, confined to signed Emitters** (D4). It must be visible.
- **`X-Stratt-Emitter-Token` stops being special** and becomes the default value of a declared field.
- **Anti-replay is still absent**, including for the shared-token half — a captured POST can be
  replayed. True before this ADR and true after; named here so it is not read as solved.

## Verification

Not shippable on assertion. This ADR owes:

- unit: a token presented in a DECLARED header authenticates; the same token in the default header
  does not, once a header is declared (a declaration that widened what is accepted would be worse
  than the gap);
- unit: an Emitter declaring no `token` block accepts `X-Stratt-Emitter-Token` exactly as before —
  the regression that matters, since every shipped Emitter is that case;
- unit: **the body verified is byte-identical to the body received** (D3), proven with a payload whose
  re-serialization differs from its original bytes;
- unit: a wrong signature, an absent header, and an unreachable verifier each REFUSE, and the refusal
  is distinguishable in the log while telling the caller nothing (§1.8 for the operator, nothing for
  the attacker);
- unit: core never passes key MATERIAL over the port — only coordinates (the §2.5 property, asserted
  against the request the port actually sends);
- **live**: a real signed POST, verified against a key held in OpenBao that the control plane never
  reads, launching a Run — and the same POST with one byte of the body changed, refused.

### All of it, paid (2026-08-03)

`demos/network-device` gains the OpenBao plugin as its **MAC verifier** — not for certificates: the
NMS signs its batched reports and the control plane checks them against a key it never holds. The
signing key is created with `exportable: false`, so nothing (`strattd` included) can read it back
out of OpenBao; verification therefore cannot be happening anywhere but inside it.
`task demo:network-device:run` EXIT=0:

```
demo: seed the NMS signing key inside OpenBao (the control plane never reads it)
  transit key nms-webhook exists, and exportable=false — nothing can read it back out
  a signature over different bytes is REFUSED (401)
  the correctly signed report is accepted — verified against a key strattd never read
  one POST became five events and crossed the threshold — exactly ONE Run
```

**The refusal is asserted FIRST, and that ordering is the point.** A verifier that returned true
unconditionally would pass every other line in that block; only the tampered case makes the accepted
one evidence. The tamper changes one field of the body and signs THAT, so what is being caught is a
body/signature mismatch rather than a missing header.

**Falsified**, each mechanism removed in turn: verifying a re-serialized body instead of the raw
bytes (D3), no provider bound degrading to unverified ingest, an unreachable provider treated as a
pass, and verification skipped entirely for a signed Emitter. D1's three guards falsify too —
ignoring the declared header, merely trimming the prefix rather than requiring it, and a declared
header that widens acceptance instead of replacing the default.

**The §2.5 property is asserted against the request the port actually sends**, not against the
intention: the test reads back what crossed and checks it carries a key COORDINATE and an algorithm,
with no field for material and nothing secret in it.

### What building it found

- **A declared prefix was merely trimmed, not required.** `strings.TrimPrefix` is a no-op when the
  prefix is absent, so an Emitter declaring `"Bearer "` would have authenticated a caller that sent
  a bare token — widening acceptance past what the declaration says, which is the exact property the
  header rule exists to hold. Found by the test, fixed in the code.
- **The ingest handler had no test at all**, which is how a hardcoded header survived this long in
  the door that authenticates untrusted callers. It now takes its store and bus as interfaces so the
  door itself is exercised.
- ~~**`WrapKey`/`UnwrapKey` are not actually served over the site relay**… a pre-existing gap in
  ADR-0100's cross-DC story.~~ — **that characterization was wrong**, corrected by
  [ADR-0166](0166-a-key-custodian-does-not-travel.md). The far end does answer "unknown method", but
  reading the call graph rather than the constant list shows `siterelay.NewClient` is built in one
  place — dispatching plugin work to a Site — while the custodian and MAC verifier each hold their
  own direct dial. **No shipping path routes a custody call through a relay**, so those client
  methods are interface padding, not a broken capability. Nor should they travel: relaying WrapKey
  would carry the hub's DEK across the WAN, inverting the property ADR-0100 exists to provide. They
  now refuse at both ends with the reason.
