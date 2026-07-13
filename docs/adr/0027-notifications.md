# ADR 0027 — Notifications (outbound Run/Finding/Gate alerts)

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-2 "notifications"), §2.2 (Connector triad —
  Emitter is inbound; delivery is Action-shaped), §2.5 (secrets — material only
  in execution pods at spawn), §1.4 (boring spine, reuse not duplication),
  §1.8 (never hide failure), §2.4 (no implicit precedence), §1.2 (projections),
  §2 (vocabulary); ADR-0018 (Emitter/Trigger ingest), ADR-0019 (Findings),
  ADR-0009 (CredentialRef), ADR-0011 (Gates)

## Context

The last item on the Phase-2 board (§8). Stratt already turns *external*
happenings into Runs (Emitter → Trigger, ADR-0018); this slice is the **outbound
mirror** — turning notable *internal* happenings (a Run fails, a drift Finding
opens, a Gate waits for approval) into deliveries to where operators watch. It
extends "never hide failure" (§1.8) past the UI: an alert nobody sees is a silent
failure.

The consume side was well-supplied (`events.Bus` durable consumers + the
`triggerengine.Engine` daemon as a template); the **sink side was greenfield**.
"Emitter" (§2.2) is the *inbound* ingest concept, so the outbound path gets its
own delivery-plane nouns.

## Decision

1. **Delivery-plane nouns, not core-model Named Kinds.** Package `notify`;
   identifiers `notify_`-prefixed (as `awx_` kept compat identifiers out of the
   frozen §2 vocabulary). Two CaC-declared, graph-projected docs reconciled like
   Emitter/Trigger/Baseline (§1.2 — Git declares; the row is a rebuildable
   projection):
   - **Sink** (`graph.notify_sink`) — a delivery endpoint. v1 kind = `webhook`
     (generic JSON POST). Named **Sink**, not "Channel": `channel` is load-bearing
     in the core model as `mgmt.channels` (the capability-routing Facet, §2.4), and
     reusing it would be a namespace collision (charter-guardian ruling).
   - **Subscription** (`graph.notify_subscription`) — binds notice-kinds × a CEL
     `match` → a Sink, reusing the `rules` engine. **Every** matching Subscription
     fires — additive fan-out, no priority/precedence field (§2.4, the anti-GPO
     axiom).
2. **Notices** — a transient internal signal `types.Notice{Kind, At, Subject,
   Payload}` (kinds `run.failed`/`run.canceled`, `finding.open`, `gate.pending`)
   on a new `STRATT_NOTICES` JetStream stream, published from the existing
   `FinishRun` / baseline-check / `CreateGateRecord` activities (which already hold
   the Bus). `NoticeHash` (content-addressed, At-independent) is the publish dedup
   axis, so Temporal activity retries don't double-notify. `finding.open` fires
   only on the **pending→open transition** — `RecordBaselineObservations`'
   `ObservationOutcome` now returns the findings that transitioned, so a
   re-observed open Finding stays quiet.
3. **§2.5 — the load-bearing decision: the credentialed POST runs in a pod, not
   the daemon.** A webhook URL/token is a control-plane-side secret. The prevailing
   k8s-native tools resolve this in-controller (Argo CD `$secret-key`, Flux
   `secretRef`) or persist it encrypted in Postgres (AAP/AWX). **None govern
   Stratt**: §2.5 is stricter — "material never persists; injected only into
   execution pods at spawn." So delivery is modeled as an **Action** (§2.2)
   executed through the existing `dispatch`→Job→credential-injection path (§1.4 —
   reuse the spine, don't duplicate it): a `webhook` **Actuator** (hand-written
   input Contract, runs the POST in the EE image) dispatched by the notifier with
   the Sink's CredentialRef. The daemon resolves the ref to a **mount pointer only**
   (`k8s-secret` backend; others fail loudly); the kubelet injects the url/token as
   files into the delivery pod, which reads `/runner/credentials/webhook/{url,token}`
   and POSTs. **Material never enters the control plane.** (Delivery is dispatched
   directly, not via `RunAgainstView`, which requires ≥1 estate target — a
   notification has none.) **One authz model (§1.6/§2.5):** a Sink declares the
   `principal` deliveries authenticate as, and the notifier runs the **same
   `use on credential_ref:<name>` check the Run path enforces** before minting the
   mount — delivery cannot bypass the credential's `OwnerTeam` scoping (a Sink can
   only fire a credential its Principal is granted `use` on). This closes the
   charter-guardian's flagged gap rather than resting on CaC review alone.
4. **§1.8 — failure is loud and queryable.** Every delivery attempt is recorded on
   the `notify_delivery` status surface (`delivered`|`failed` + detail, readable
   like Findings) and logged. Transient infra failure (pod could not spawn)
   redelivers (JetStream nak); a poison per-delivery problem (bad Sink ref, CEL
   error, endpoint rejection) is recorded and dropped — visible, but never a
   redelivery storm. The webhook driver emits only the HTTP status + a **sanitized
   failure class** (never `str(e)`, which embeds the target URL) — §1.8 without
   leaking the secret (§2.5). The Intent→Run→task-event descent stays the
   authoritative diagnosis path; notifications are additive to it.

## Consequences

- **Live-verified (dev harness, real kind + EE):**
  - `run.failed`: a script Run against `dev-vms` exited 1 → `run.failed` Notice →
    the `e2e-runfail` Subscription matched → the notifier dispatched a delivery Job
    (`stratt-run-ntfy-e2e-runfail-…`) → the pod read the k8s-Secret-injected
    url/token and POSTed the rendered notice to a host receiver, which saw
    `Authorization: Bearer <token>` (**the credential reached the pod**) and the
    body `run <id> status failed on view dev-vms`. `notify_delivery` = `delivered`.
  - `gate.pending`: the `quarantine-gated` Workflow's Gate opened → `gate.pending`
    Notice → delivered the same way. `notify_delivery` = `delivered`.
  - **§2.5 non-leak:** neither the token nor the secret URL ever appeared in the
    strattd/daemon logs.
  - `finding.open`: emission is covered by the real-postgres integration test
    (`OpenedFindings` fires exactly on pending→open, not on re-observation); it
    uses the identical, twice-proven-live delivery path.
- **Read-only definitions:** Sinks/Subscriptions are CaC-only (§1.2); the delivery
  Job is the only runtime side effect.
- **Deferred / fast-follow (documented, not faked):**
  - Typed `slack` / `smtp` / `pagerduty` Sink drivers (webhook reaches them via
    their incoming-webhook URLs today).
  - **Delivery weight:** a pod spawn per notification is the v1 cost. Batching per
    (Sink, notice-window), 5xx retry/backoff, and a long-lived egress-injection pod
    the daemon cannot read are the optimizations. The pod-per-notification tension
    is the accepted v1 trade for strict §2.5 compliance.
  - A dedicated audit stream shape for deliveries (v1 records on `notify_delivery`
    + structured logs); additional notice kinds (Gate decided/expired, orphan
    Findings, max-delta gate); per-Principal preferences / digest batching.
- **Charter posture:** delivery credentials inject into the pod at spawn, never the
  daemon (§2.5 — stricter than AAP); delivery reuses the dispatch spine (§1.4);
  additive fan-out with no precedence (§2.4); failure is queryable + loud (§1.8);
  Sinks/Subscriptions are projections, Notices transient (§1.2); `notify_`-scoped
  delivery-plane nouns, `Sink` not `Channel` (§2).

## Runway after
Phase 2 is complete. Next: Phase 3 — Sites (NATS leaf), full OpenFGA
(View-scoped execution), object-locked Evidence store + CIS pack, `Intent/*`
kinds GA; plus the deferred notification surfaces above.
