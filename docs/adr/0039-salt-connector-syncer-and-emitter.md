# ADR 0039 — Salt Connector: grains Syncer + event-bus Emitter

- **Status:** Accepted (Commit 1 — Salt grains Syncer; Commit 2 — Salt event-bus Emitter)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.2 (Connector = some combination of Syncer/Action/Emitter; Emitter =
  "poller, stream subscriber"), §2.1/§2.4 (facet-ownership; no implicit precedence), §1.2 (projections,
  never a second truth), §1.4 (boring spine, few boring deps), §1.6 (one event model / one trigger
  spine), §2.5 (credential material never persists), §0 (market context — Salt under Broadcom);
  ADR-0037/0038 (Chef, Puppet — the pattern this completes), ADR-0018 (Emitters + trigger engine)

## Context

Third and final connector in the config-mgmt Syncer track (Chef ✓ → Puppet/OpenVox ✓ → **Salt**). Salt
is **not** in the org estate (they run Chef), so this is OSS product breadth + the last generality
proof — **harness-only** (a `saltsim`). The steward chose the larger scope: a grains **Syncer** *and*
an event-bus **Emitter** — one Connector shipping two capabilities (§2.2), and the **first
stream-subscriber Emitter** (the existing Emitters are inbound push-webhook receivers).

**Salt status (verified, 2026).** Apache-2.0, actively maintained by Broadcom-employed core maintainers,
fresh **3008 LTS (May 2026)**, predictable 1-LTS/year lifecycle. The charter's "maintenance under
Broadcom is in open question" has resolved toward **"maintained, but single-vendor"** — no vendor-neutral
foundation, no community hard-fork (unlike Puppet→OpenVox). Honest read: **live**, the risk is *strategic*
(Broadcom's post-acquisition free-tier pattern), not imminent — and structurally hedged: a read-only
HTTP boundary + a permissive license mean a fork or exit costs nothing. Reasonable to build; target the
3008 LTS line.

## Decision

1. **Grains Syncer on the shipped spine (Commit 1).** `connectors/salt/` mirrors `connectors/puppet`.
   Enumeration is the **runner `cache.grains`** lowstate (`{client:"runner", fun:"cache.grains",
   tgt:"*", tgt_type:"glob"}`) via salt-api — it reads the **master's grain cache** with **no minion
   round-trip**, immune to dead minions. The cache can be stale; for a rebuildable read-model (§1.2)
   that's the correct, non-invasive trade (a `client:"local"` force-refresh is a documented deferral).
   `tgt` is mandatory since Salt 3001, always sent. Normalize → `UpsertEntities` via
   `NormalizerProjector()` only → `TombstoneAbsent("salt.minion_id", seen)`.

2. **Third distinct auth model, still zero deps (§1.4).** salt-api external-auth (eauth): `POST /login
   {username,password,eauth}` → an `X-Auth-Token` header carried on subsequent calls, re-login on 401.
   Plain HTTPS + a token — `net/http` suffices. Across the track: **Chef Mixlib-RSA (go-chef) → Puppet
   mTLS (stdlib) → Salt eauth token (stdlib)** — three Sources, three auth schemes, three query APIs,
   one Normalizer discipline, **no vendor lib beyond Chef's unavoidable crypto**. The abstraction
   generalized; that was the point of the generality test.

3. **Source-scoped facets, no labels (the ADR-0037/0038 discipline).** `Kind: host`; identity
   `salt.minion_id` (always) + `dns.fqdn` (correlation key). Facets are source-scoped
   `salt.node.identity/os/network` (curated charter-down from grains; list-valued `ipv4`/`ipv6` take
   first; uncovered until a Contract demands a schema). **No Entity labels** — the shared label bag is a
   whole-set last-writer projection that clobbers across correlating Sources (§2.4); selectable data
   rides the facets and the example View selects via `FacetPredicate` on `salt.node.identity.os_family`
   (Salt has no `environment` grain — any core grain demonstrates the story).

4. **Event-bus Emitter — the first stream-subscriber (Commit 2).** A long-lived goroutine
   outbound-connects to salt-api `GET /events` (SSE), parses each `tag:`/`data:` frame, filters by a
   configurable tag-prefix allowlist, and **publishes a `types.EmitterEvent` onto the existing
   emitter-event stream** via `Bus.PublishEmitterEvent`. The entire downstream — trigger engine, CEL
   match, cooldown, deterministic launch — consumes it **unchanged** (§1.6, one event model / one
   trigger spine). **No trigger/emitter-registry change:** Triggers match an emitter by name string and
   do not require it to be registered, so no new emitter kind, no `ValidateEmitter`/`ValidateTrigger`
   change. Reconnect with backoff on stream end / token expiry.
   - **CEL payload contract:** the engine binds `event` = the flat `Payload`, so the Emitter sets
     top-level keys `{tag, stamp, data}` → `when` expressions see `event.tag`, `event.stamp`,
     `event.data.*`.
   - **Dedup-safety:** `EventHash` = `sha256(emitter + "|" + json(Payload))` and excludes `ReceivedAt`;
     including the Salt `_stamp` (and full `data`, carrying `jid` for job events) makes genuinely
     distinct events hash distinctly, so the JetStream dedup window won't drop them.
   - **Flooding:** the Salt bus is high-volume; the tag-prefix filter is the source-side guard, and
     narrowing it is recommended (empty = forward all).

## Charter posture

- **§2.2** one Connector, two capabilities; the Emitter is the charter's literal "stream subscriber"
  Emitter, feeding one trigger spine (§1.6) — not a parallel event path.
- **§2.1/§2.4** source-scoped `salt.node.*` (one owner each); entities unify via `dns.fqdn`; no shared
  namespace, no cross-source precedence; no Entity-label writes.
- **§1.2** read-only projection; the Salt master stays the SoR; grain-cache staleness is an explicit,
  acceptable read-model trade; full-enumeration tombstones. Not a writable CMDB.
- **§1.4** zero new dependency (stdlib HTTP + token + SSE). The Emitter is decoupled from NATS behind a
  one-method `eventPublisher` interface (the real `*events.Bus` satisfies it; tests use a fake).
- **§2.5** salt-api creds from env, never persisted/logged; event payloads carry Salt event data (tags,
  job returns), not secrets. CredentialRef brokering for Syncers is the shared follow-up.
- **§2 vocabulary** Kind `host`, identity `salt.minion_id` + `dns.fqdn`, facets `salt.*` — data;
  "grains"/"minion" are Salt tool content; **Emitter** used as the Named Kind; no banned
  `inventory`/`resource` in identifiers.

## Alternatives considered

- **`client:"local"` grains.items enumeration.** Rejected as the default: it executes on every minion,
  blocks on dead ones, needs exec perms. The runner grain-cache is the non-invasive projection read;
  local force-refresh is a documented deferral.
- **Registering the Salt emitter as a first-class `types.Emitter` kind.** Unnecessary for triggers
  (name-string match); deferred as UI/discoverability polish.
- **A vendor Salt SDK.** None needed — salt-api is plain HTTP + a token header (and SSE), all stdlib.

## Reviews

- **charter-guardian: PASS** (no must-fix). Non-blocking flags, addressed or accepted: (1) the
  stream-subscriber publishes under an emitter name with **no `types.Emitter` registration and no token
  boundary**, and Triggers match by bare string — so a webhook Emitter registered under the same name
  would feed the same Triggers. Not a security hole (launch authz is the Trigger's Git-declared
  `Principal`), but the name shares the emitter routing namespace and the subscriber is absent from any
  registry/UI listing — see the emitter-kind-registration deferral below. (2) `provenance().At` is
  **projection time, not observation time** — a stale grain-cache entry gets a fresh-looking stamp; an
  honest read-model trade, liveness deferred. (3) the empty-`EventTags` = forward-all default now
  **warns loudly** when the Emitter is enabled without a tag filter (flooding guard). (4) the
  `EventHash` dedup edge (identical `data` with no `_stamp`) matches existing webhook-Emitter semantics.
- **vocabulary-linter: CLEAN** (`salt.*`/`minion`/`grains` as tool content; `Emitter`/`Syncer`/`host`
  as Named Kinds; no banned identifiers; `salt/job/…` only in vendor event-tag strings).
- **No dependency-scout** — zero new dependencies (stdlib HTTP + token + SSE).

## Honest deferrals

- `client:"local"` force-refresh + `manage.up` liveness cross-check (and, relatedly, carrying the
  master's per-minion cache timestamp so provenance freshness is self-describing rather than
  projection-time); `hwaddr_interfaces` MAC facet; event-bus flooding guards beyond the tag filter
  (batching/rate-limit); **first-class emitter-kind registration** for the Salt stream subscriber
  (reserve/validate its emitter name against the Emitter registry so it can't collide with a
  token-authed webhook Emitter, and so it lists in the UI); a real-Salt e2e (harness-only build —
  `saltsim` is the proof surface); pinning `salt.node.*` schemas once a Contract demands them; the
  carried platform deferrals — per-key Entity-**label** ownership (ADR-0038) and cross-source Entity
  **liveness** (tombstone/resurrect, ADR-0038).

## Consequences

The config-mgmt ingest track is complete: three Sources (Chef, Puppet/OpenVox, Salt) project into one
typed graph under one Normalizer discipline, proven vendor-neutral across three auth schemes with no
vendor lib beyond Chef's unavoidable crypto. Salt adds the first **stream-subscriber Emitter**, wiring
real-time estate events into the shipped trigger spine with no changes to it — establishing the pattern
for future streaming event sources. No new engine, no migration, no new dependency.
