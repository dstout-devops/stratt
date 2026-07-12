# ADR 0018 — Trigger engine: Emitters × CEL → launches

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.8, §2 (Trigger, Emitter), §2.2, §2.5, §3 ("NATS events × CEL → Workflow launches"), §8 (Phase 2)

## Context

The Trigger object shipped with kind `schedule` (ADR-0010). This slice adds the
event-driven half the charter architecture names, plus the parked rider: Triggers
(both kinds) can launch declared Workflows, not just single Runs.

## Decision

1. **Emitters (CaC, `emitters/*.yaml`):** `{name, kind: webhook|alertmanager,
   tokenHash}`. **The declaration holds only `hex(sha256(token))`** — nothing
   secret exists in Git or the registry (§2.5); callers present the raw token in
   `X-Stratt-Emitter-Token`, compared constant-time. Unknown emitter names 401
   like bad tokens (the ingest surface enumerates nothing to the unauthenticated).
2. **Ingest** (`POST /emitters/{name}`, mounted outside `/api/v1` — alert sources
   are not Principals): body-limited, published to the new JetStream stream
   `STRATT_EMITTER_EVENTS`. `webhook` = one event per POST; `alertmanager` parses
   the AM webhook shape and **explodes `alerts[]`** — one event per alert with
   receiver/groupLabels folded in, so CEL rules match per alert.
3. **Trigger kind `event`:** `{emitter, when (CEL), cooldownSeconds?, launch
   target}`. Launch target for BOTH kinds is now `viewName`+Step fields XOR
   `workflowName` (workflow launches carry no Step fields — the Workflow declares
   its own). **CEL compiles at declaration parse** with a static worst-case cost
   gate — a bad or absurd rule fails its file at plan/reconcile, never at event
   time (§1.8); the environment exposes `event` (payload) and `emitter`.
4. **CEL:** `github.com/google/cel-go` v0.29.2 (dependency-scout RECOMMEND —
   hermetic, non-Turing-complete, the K8s/Envoy standard; note: repo moved to the
   cel-expr org 2026-06, import path unchanged; pre-1.0 minors go through the N-1
   fixture check). Both scout-mandated bounds implemented: `EstimateCost` at
   compile, `CostLimit` per evaluation. **Rule evaluation errors are logged and do
   not launch — never a silent false** (§1.8).
5. **Delivery + dedup:** durable JetStream consumer, at-least-once. Launches are
   idempotent by construction: the Temporal workflow id derives from
   `trigger name + event content hash`; a redelivery (or an Alertmanager webhook
   retry — the hash deliberately excludes ReceivedAt) hits Temporal's
   already-started rejection instead of double-launching. A genuinely new
   occurrence of an identical payload still fires once the prior launch closed.
   Undecodable stream messages Term (never redeliver); infrastructure errors Nak.
6. **Cooldown:** optional per-trigger seconds suppressing matches after a launch —
   storm damping. In-memory (single-replica posture, ADR-0013); a restart resets
   cooldowns. Recorded, not hidden.
7. **Workflow launches:** `RunDAG` gained `EnsureWorkflowRun` (the ADR-0010
   pattern): trigger- and schedule-started executions create their own
   `workflow_run` row, stamping the new `triggered_by` column — the §1.8
   Trigger → WorkflowRun rung. The schedule reconciler now projects only
   schedule-kind Triggers and compiles workflow-launching schedules to RunDAG
   actions.
8. **Payload→params templating is deferred:** launches use declared params only.
   Binding event fields into Step params is the same parametrization design as
   templated Views — one decision, later, reviewed against the
   no-expression-language non-goal. (CEL itself is charter-named for Trigger
   *rules* and stays confined to boolean matching.)

## Consequences

- Showcase #3's shape is live: any alert source with a webhook → CEL rule →
  quarantine Workflow, in the same authz/audit/descent model as everything else.
- The Trigger vocabulary is now complete against its charter definition: Temporal
  Schedule, Emitter event × CEL, manual, API — all one object, one descent column.
- charter-guardian findings: launch infrastructure failures now nak the event
  for redelivery (idempotent via the deterministic ids) instead of ack-and-log;
  cost estimation fails closed. Noted, accepted: the 64-bit dedup-id prefix
  (collision-improbable at expected volumes); tokenHash readable via the
  authz-gated GET /emitters (use high-entropy tokens — a hash of a weak token
  is dictionary-attackable); Emitter modeled standalone rather than as a
  Connector capability with a Source — right for inbound webhooks, revisit
  when the Connector packaging model solidifies.
- Follow-ups: payload templating (above); per-trigger rate metrics; Emitter
  signature verification schemes beyond bearer tokens (e.g. HMAC-signed payloads,
  GitHub-style) as emitter kinds; cooldown persistence if replicas>1 lands.
