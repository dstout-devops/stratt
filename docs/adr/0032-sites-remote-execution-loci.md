# ADR 0032 — Sites: remote execution loci (NATS-leaf push; cosign/OCI pull Bundles)

- **Status:** Accepted (Commit 1 — push-mode spine; Commit 2 — pull Bundles — is a
  documented fast-follow)
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.3 (Step/execution plane; **Site** = "remote execution
  locus — satellite dispatcher + NATS leaf"; **Bundle** = "cosign-signed OCI
  artifact of content + deps for pull-mode agents"), §3 (NATS JetStream =
  "leaf-node Sites (the Receptor replacement), pull-agent transport"; K8s Jobs
  the only execution primitive), §1.4 (boring spine, no parallel stack), §2.5
  (CredentialRef — material never persists / never crosses the wire), §1.2
  (projections), §1.6 (one authz/audit), §1.8 (never hide failure; descent shows
  *where*), §1.1 (type the seams), §1.5 (pinned/hash-verified supply chain);
  ADR-0028 (View-scoped execution), ADR-0031 (the dispatch Interpreter seam this
  reuses), ADR-0016 (the JobSpec.Env state-backend credential this must fence off)

## Context

Every Run executed in the one central cluster: `RunAgainstView` resolves a View
to targets, slices them, and each `Execute` calls `dispatch.Dispatcher.Run` → a
K8s Job in the hub namespace, following the pod log via the K8s API and
publishing events to NATS. There was **no notion of *where* a target runs** —
`dispatch.go` explicitly reserved the agent/sidecar shape for "Sites (Phase 3)".
This slice opens the **fleet** half of Phase 3. Steward chose the maximal scope:
BOTH push-mode Sites AND pull-mode agent + cosign/OCI Bundles, with **per-target,
location-derived** routing.

The load-bearing seam already existed: `Dispatcher.Run(runID, slice, spec,
interpreter, creds, heartbeat)` — JobSpec in, `Result` out, stdout events → NATS.
A remote Site is a **second caller of that exact function**. NATS is already the
event backbone; a **leaf node** forwards a Site's `stratt.run.>` events into the
hub stream with no change to hub consumers. The only genuinely new NATS direction
is dispatch (hub → Site).

## Decision (Commit 1 — push-mode spine)

1. **`Site` is a first-class Named Kind, a CaC-declared projection (§1.2, §2.3).**
   `types.Site{Name, Mode(push|pull), Namespace, Description}`; `graph.site`
   (migration 00018) with the desired-state engine as sole writer, mirroring
   Emitter/Trigger. The built-in **`local`** locus (today's central cluster) is
   never declared and never a row. Live agent up/down is **ephemeral NATS KV**
   (`SITE_LIVENESS`), never a graph write — the graph stays a projection, not a
   second truth about a fact the substrate owns. `GET /sites` merges the
   declaration with live status. A Site name is a NATS subject token, so it may
   not contain `.`/whitespace/`*`/`>`.

2. **Per-target, location-derived routing (§1.2, §1.1).** A new provenance-bearing
   Facet `mgmt.site` (`{site}`), written by Syncers and merely READ by routing,
   places each Entity at a locus; unset ⇒ `local`. `ResolveTargetsBySite` reads it
   in one bulk query (`FacetValuesByEntities`) and returns targets grouped by Site,
   **sorted** (Temporal replay determinism). Unlike `mgmt.channels` (a per-capability
   co-management map, §2.4), `mgmt.site` is a single **physical** fact — one Entity
   lives at one execution locus — so a scalar is correct; it carries no capability
   precedence.

3. **The fan-out uses a GLOBAL slice index across all (Site, chunk) pairs (§1.8).**
   Event identity is `runID/slice/seq` (the JetStream MsgID). Two Sites' "slice 0"
   would **dedup-erase each other's events server-side** — silent truncation.
   Numbering slices globally keeps every event and the Job name
   `stratt-run-<run>-s<slice>` unique. `RunEvent.Site` (stamped by the dispatcher)
   and `run.sites` (the union) answer "**where** did this run" for descent.

4. **§2.5 is enforced structurally, not by review — `JobSpec.RemoteSafe()`.** The
   opentofu actuator puts a **plain** `TF_HTTP_PASSWORD` into `JobSpec.Env`
   (ADR-0016) — safe today only because the JobSpec never leaves the hub process.
   The moment a JobSpec crosses NATS or enters a Bundle, that value would leak.
   `RemoteSafe()` refuses **any** non-empty `Env`; it is checked in `Execute`
   before dispatch and again in `sitegw.Dispatch` before publish. Only credential
   **pointers** (`dispatch.CredentialMount`) travel; the agent resolves material
   against its **own local Secrets** at pod spawn (preflighted by `missingSecrets`),
   exactly as the hub's kubelet does. Consequence: **opentofu stays hub-local in
   v1** (its Env-material path can't go remote).

5. **One execution primitive, reused — the agent is a second caller (§1.4).**
   `sitegw.Gateway` adds three NATS flows: a **work-queue** dispatch stream
   (`STRATT_DISPATCH`, per-Site durable pull consumer, `MsgID=run/slice`); the
   terminal result stream (`STRATT_DISPATCH_RESULT`, awaited with heartbeats);
   and an ephemeral core-NATS cancel. `stratt-agent` connects ONLY to its local
   leaf NATS + its OWN K8s clientset, and calls the **same** `Dispatcher.Run` — no
   parallel execution stack. Events flow to its local Bus and leaf-forward to the
   hub's run-event stream unchanged. Idempotency: a Temporal retry re-dispatches
   with the same MsgID (deduped), and the agent adopts an existing Job by name
   (`AlreadyExists`); the result carries the same MsgID.

6. **Authz unchanged — routing is not a new user axis (§1.6, §2.1, ADR-0028).**
   The execution gate stays `runner`-on-View; a target's Site is derived from its
   location, not chosen by the launcher, so no new grant is required to reach
   correct-by-construction routing. **The control point for *where* a workload
   runs is therefore the `mgmt.site` Facet-ownership registry entry** (§2.1: one
   declared write owner per Facet namespace, scoped by View) — whoever may write
   `mgmt.site` governs placement, not an execution grant. The agent is an
   `agent`-kind Principal. Cancellation is Site-aware: `CleanupRun(runID, sites)`
   deletes hub Jobs and signals each remote Site.

## Consequences

- **Verified this commit:**
  - **Unit:** `RemoteSafe` refuses Env material without leaking a value;
    `ResolveTargetsBySite` groups deterministically; the fan-out allocates
    **globally-unique** slice indices across two Sites and records the Site union
    (`TestRunAgainstViewFanOutBySite`); `ValidateSite` rejects dotted/wildcard names.
  - **Integration (real NATS):** the full gateway round-trip — `Dispatch` → the
    Site consumes → `PublishResult` → `AwaitResult` — plus the liveness KV, the
    cancel signal, and the §2.5 **refusal of a JobSpec carrying Env material at the
    dispatch door** (`core/internal/sitegw/gateway_test.go`).
  - Full build + all module tests + lint green.
- **Runnable-next (harness wired, not yet executed):** the cross-cluster kind
  fan-out e2e — `task dev:site:up` brings up Site "edge-west" (namespace `site-b`,
  a leaf `nats-site`, a Site-local credential Secret). A View spanning `local` +
  `mgmt.site=edge-west` should fan out; the `site-b` Job runs under the agent's own
  clientset (the hub never touches that namespace); events leaf-forward with
  `site=edge-west`; `run.sites` lists both; the dispatch payload + logs carry no
  credential material. The local-Site path is byte-identical to the already-e2e'd
  execution path; the new transport is proven by the live integration test above.

## Deferred / fast-follow (documented)
- **Commit 2 — pull-mode + cosign/OCI Bundles:** an egress-only Site pulls signed
  Bundles from an OCI registry, verifies cosign signature + pinned digest before
  unpack (a tampered Bundle is refused, §1.8/§1.5), and runs them via the same
  `Dispatcher.Run` (`stratt-agent --mode=pull`). Adds `oras-go` + `sigstore-go` +
  a registry image (dependency-scout gates them). The agent already refuses pull
  mode until then.
- **Interpreter / trust-tier distribution — the deepest tension:** the agent is
  *compiled* with in-tree Interpreters only (Bundles carry content, not the Go
  Interpreter); a `verified`/`community`-tier actuator reaching a Site, and
  hub↔agent `Interpret()` version skew, are unresolved. Trust tiers stay
  unimplemented (as everywhere).
- **opentofu-at-a-Site** (needs the state cred expressed as a CredentialRef pointer
  so `RemoteSafe` passes).
- **Lossy-leaf on partition (§1.8) — disclosed, terminal-correctness preserved.**
  The *live* run-event tail is leaf-forwarded over core-NATS subjects, so a leaf
  partition can **gap the live diagnostic stream**. Terminal correctness does NOT
  depend on it: results flow on the durable `STRATT_DISPATCH_RESULT` JetStream
  (store-and-forward; `AwaitResult` uses `DeliverAll`), and the agent Job lease
  backstops cancellation — only the live tail can gap, and it recovers on
  reconnect. The prod-correct fix (site-local JetStream + hub stream-sourcing, a
  heavier multi-domain topology, or JetStream-backed event forwarding with replay)
  is deferred, not silently assumed away.
- **Per-Principal site-dispatch authz** (an `action`/`site`-object grant); **remote
  `CredentialMount` namespace rewriting** — the hub resolves `SecretNamespace` from
  the CredentialRef locator and it travels verbatim; a hub-named namespace that
  does not exist at the Site yields a clean terminal "secret not present"
  (`missingSecrets`) — no material leaks and the failure is visible (§1.8/§2.5) —
  but cross-Site namespace mapping is genuinely unsolved.

## Charter posture (surfaced)
- **§1.4:** the Site is a second caller of the one execution primitive; NATS leaf
  is the Receptor replacement; no parallel stack.
- **§2.5:** only pointers cross NATS or (Commit 2) enter a Bundle — `RemoteSafe` is
  the structural gate; material resolves into the pod at spawn by the Site's own
  secret store, never in the platform, never on the wire.
- **§1.2:** `mgmt.site` is a provenance-stamped Facet (read-only in routing); Site
  declarations are CaC projections; live status is ephemeral KV, not a graph write.
- **§1.8:** descent shows *where* each target ran; global slice numbering prevents
  event dedup-truncation; the lossy-leaf limitation is disclosed with its fix.
- **Non-goals:** no writable CMDB, no second truth, no MDM/imaging, no new config
  language, no paid tier.
