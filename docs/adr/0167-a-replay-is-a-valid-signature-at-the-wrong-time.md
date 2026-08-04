# ADR 0167 — A replay is a valid signature at the wrong time

- **Status:** **Accepted** (2026-08-04, steward) — **live-proven**: a genuinely valid signature,
  thirty minutes old, refused by a real cluster. See Verification. Charter review by hand — this session's rules bar
  the subagent; §1/§1.8/§2.4 answered inline. **No new dependency, no port change, no migration.**
- **Date:** 2026-08-04
- **Deciders:** steward
- **Charter sections:** §1 (no new configuration languages), §1.8 (never hide diagnosis), §2.4 (no
  implicit precedence)
- **Pays [ADR-0164](0164-a-source-signs-and-the-core-does-not-hold-the-key.md) D5**, which booked
  timestamped schemes rather than approximating them.

## Context

ADR-0164 made a signed source verifiable. It deliberately left two things, and they are one thing:

> **Timestamped anti-replay schemes** (Stripe's `t=…,v1=…`, which signs `timestamp.body` and demands
> a tolerance window). **Booked, not built.** … Anti-replay is still absent, including for the
> shared-token half — a captured POST can be replayed.

### Dedup is not freshness, and the difference is the whole ADR

Stratt already deduplicates identical events. `EventHash` covers emitter + payload and deliberately
excludes `ReceivedAt`, so a retried POST collides — on the JetStream publish (`WithMsgID`) and again
on the derived Temporal workflow id. The existing comment states the limit precisely:

> A genuinely new occurrence of an identical payload still fires later — JetStream's dedup window is
> short and Temporal only rejects the id while the prior launch is running.

That is a **correctness** control, and a good one: Alertmanager retries and a retry must not launch
twice. It is **not** a security control, because an attacker picks the moment. Wait out a short
window and the same bytes, with their still-valid signature, are accepted as new.

**ADR-0162 sharpened the consequence without anyone noticing.** A Trigger declaring `count: 5` now
accumulates, so a replayed event does not merely re-fire something idempotent — it **advances a storm
counter**. Five replays of one captured flap manufacture an incident that never happened, and the
resulting Run is indistinguishable from a real one.

### And half the sources cannot be verified at all yet

Stripe and Slack sign `timestamp + body` and carry both in one header (`t=…,v1=…`). ADR-0164 verifies
a MAC over the body alone, from a header that is nothing but the signature, so those sources remain
unreachable — not for want of a signature check, but for want of the SHAPE.

## Decision

### D1 — The signed payload and the header shape are DATA, from closed sets

```yaml
verify:
  header: Stripe-Signature
  format: kv # raw (default) | kv — "t=…,v1=…"
  signatureKey: v1 # which pair holds the MAC (kv only)
  timestampKey: t # which pair holds unix seconds (kv only)
  signedPayload: timestamp.body # body (default) | timestamp.body
  toleranceSeconds: 300
  algorithm: hmac-sha256
  keyRef: stripe-webhook
```

**`signedPayload` is an ENUM, not a template**, and that is the §1 line. `"{timestamp}.{body}"` would
be a two-token templating language — small, and then someone wants `{header:X}` and a separator and
an ordering, and the estate has an expression evaluator nobody decided to build. Two named shapes
cover every scheme worth supporting; a third is a one-line addition and a review.

### D2 — Freshness is the anti-replay control, and it REFUSES

A request whose declared timestamp is outside `toleranceSeconds` is refused, before the MAC is
checked — there is no point asking a KMS about bytes we will not accept.

**This is the actual defence.** A captured request stops being usable once its timestamp ages out,
which bounds the attack to the tolerance rather than to "forever, past the dedup window". The
tolerance is declared because only the operator knows their clock skew and their source's retry
behaviour; there is no safe default to pick for them.

### D3 — Freshness applies wherever a timestamp is DECLARED, and nowhere else

An Emitter that declares no timestamp gets today's behaviour exactly. That is not timidity: without a
signed timestamp, any freshness field an attacker can edit is worthless, and one they cannot edit
does not exist. **A timestamp is only a defence when it is inside what was signed** — which is
exactly what `signedPayload: timestamp.body` guarantees and what a bare `Date:` header does not.

So the shared-token half of ADR-0164 D1 gains nothing here, and the ADR says so rather than implying
coverage it does not have.

### D4 — A nonce store is DECLINED

Remembering every signature seen would close the window inside the tolerance too. Refused:

- it is a second `trigger_window`-shaped table with far worse cardinality — one row per request
  rather than per (trigger, key) — on the hot path of the surface whose job is to accept reliably;
- it must be shared across replicas to work, so every ingest becomes a write;
- and it buys the interval between a request and its own expiry, against an attacker who already
  needs to capture ciphertext-authenticated bytes in flight.

The tolerance is the knob. If someone needs replay-proofing inside it, that is a different design
(idempotency keys carried by the source), not a bigger table.

## Consequences

- **Stripe- and Slack-shaped sources become reachable**, which was the concrete half of the gap.
- **A signed source can bound replay**; an unsigned one still cannot, and D3 says so plainly.
- **The parity register's "replay protection is absent" becomes wrong in one direction and right in
  another**, and both need saying: retry dedup exists and always did; freshness did not and now does,
  for declared-timestamp sources only.
- **One more ordering constraint on the ingest path**: freshness before MAC, MAC before parse. Each
  is cheap and each is refusable, which is the order that leaks the least work to an attacker.

## Verification

- unit: a `kv` header is split, and the declared keys select the signature and the timestamp — a
  scheme core has never seen, expressed as data;
- unit: `signedPayload: timestamp.body` signs `<t>.<body>` **byte for byte**, proven with a body
  whose re-serialization differs from its own bytes (ADR-0164 D3 still holds);
- unit: a timestamp outside tolerance is **refused before the verifier is consulted** — asserted by a
  verifier that fails the test if it is called at all;
- unit: a timestamp inside tolerance passes, and one in the FUTURE beyond tolerance is refused too
  (clock skew cuts both ways, and only refusing the past is a half-check);
- unit: an Emitter declaring no timestamp behaves exactly as ADR-0164 shipped it — the regression
  that matters, since every signed Emitter that exists is that case.

### Paid (2026-08-04)

`demos/network-device` gains `nms-timestamped`, a second signed Emitter carrying the Stripe/Slack
shape. A second one rather than changing `nms-batch`, so the demo asserts ADR-0164's guarantee has
not been traded away for this one. `task demo:network-device:run` EXIT=0:

```
demo: assert a correctly signed but STALE request is refused as a replay
  a freshly signed request is accepted
  …and the same signature 30 minutes old is REFUSED — valid, but at the wrong time
  …and so is one 30 minutes in the future — skew is refused both ways
```

**The middle line is the whole ADR.** That request is signed with the same key, over its own
timestamp, so the MAC is genuinely valid — OpenBao computed it. It is refused only because the clock
says it is old. A verifier that checked the signature and ignored the time would accept it, and that
is the replay this exists to stop.

The future case is asserted beside it because only refusing the past is a half-check: an attacker who
can push a clock forward would otherwise mint requests that never expire.
