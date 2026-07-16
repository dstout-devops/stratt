# ADR 0045 — DB-driven Syncer instantiation & Connector home-ownership gate (full re-home auto-cutover)

- **Status:** Proposed (design deferral spun out of ADR-0044 slice 7; not yet scheduled). Captures the
  Connector-architecture change required to make a cross-Cell Source re-home a fully control-plane-driven
  cutover with **no manual Connector redeploy** on the destination Cell.
- **Date:** 2026-07-16
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2 (projection, single writer), §1.4 (boring spine), §2.1/§2.4 (exactly one
  answer / no implicit precedence), §1.6 (one model). Extends ADR-0044 (Cells) and the ADR-0014 Connector/
  Syncer model.

## Context

ADR-0044 slice 7 shipped **fenced cross-Cell Source re-home** — the correctness core: a Source is sealed
on its home Cell (a DB write-path fence rejects its Normalizer projections), the destination Cell adopts
it, and the source Cell tombstones the now-unobserved Entities. That slice deliberately scoped the
**destination-side cutover** as a runbook step: an operator deploys the Source's Connector on the
destination Cell, and the control plane makes the hand-off safe *around* that deploy.

The reason is an architectural collision surfaced during slice-7 implementation. **Syncers are
env-instantiated, not DB-driven.** A Connector runs on whichever daemon carries its env config
(`STRATT_VCENTER_URL`, …); on boot it `RegisterSource()`s (`ON CONFLICT (name) DO UPDATE SET cell = <this
daemon's cell>`) and runs its own leader-gated loop ([`core/cmd/strattd/main.go`], the per-Connector
`syncer.Register()` / `syncer.Run()` blocks). There is **no central controller that drives Syncers from
`graph.source` rows.** Consequently, if the destination Cell's Connector is simply deployed, it begins
syncing the Source immediately — while the source Cell may still be syncing it — a **double-writer window**
the slice-7 fence (which is per-Cell, on separate Postgres) does not close on its own.

Fully automating the cutover therefore requires the Syncer to become **home-aware**: a Connector must
project a Source's Entities **only if it is the Source's declared home Cell and the Source is not sealed** —
i.e. the fence generalized from "reject a sealed Source's writes" to "a Syncer stands by unless it owns an
unsealed Source." That is a change to how every Connector instantiates and gates, so it is its own ADR.

## Decision (proposed)

Introduce a **Connector home-ownership gate** so a Syncer projects a Source's Entities **iff** the Source's
`graph.source.cell` equals this daemon's Cell **and** `rehoming_to IS NULL`:

1. **DB-driven standby.** A deployed Connector whose Source is homed on a peer Cell (or is sealed) enters a
   **standby** loop: it neither `RegisterSource()`-claims the Source nor projects, it only watches the
   Source's home/seal state. It begins syncing the instant the Source's home flips to this Cell and the
   seal clears — the destination-side of a re-home becomes automatic, no redeploy.
2. **A shared gate, not per-Connector logic.** The check lives once (in the shared projection / register
   path the Connectors already call — `Store.RegisterSource` + the Normalizer projector), so no
   Connector-specific code decides ownership. This keeps §1.4 (boring spine) and §2.4 (one answer — the
   `graph.source.cell` column is the single home authority).
3. **Re-home cutover becomes control-plane-only.** `RehomeSourceWorkflow` (ADR-0044 slice 7) no longer
   needs the "deploy the Connector on B first" runbook precondition when B already runs the Connector in
   standby: adopt flips `graph.source.cell = B`, B's standby Syncer wakes and re-projects, the source Cell
   tombstones — end to end without a human deploy step.

## Consequences

- **Blast radius:** touches every in-tree Connector's Syncer loop (awsec2, vcenter, msgraph, chef, puppet,
  salt, certissuer) via the shared gate, plus a standby state and a home-flip watch. This is why it is
  deferred out of slice 7 rather than bolted on.
- **Credentials still deploy-bound (§2.5):** a standby Connector still needs its `CredentialRef` resolvable
  against the destination Cell's Secrets; the material never ships in the re-home payload (only the
  CredentialRef name). So "no manual redeploy" means no *Connector* redeploy — the destination must still
  hold the destination-local Secret, which is a deploy/secret-management fact, not a Connector one.
- Until this lands, ADR-0044 slice-7 re-home is complete and correct with the one-line runbook step
  (deploy/enable the Connector on the destination Cell); the fence guarantees no double-writer regardless.

## Reviews

- Spun out of the ADR-0044 slice-7 charter-guardian design review (which confirmed the Source is the
  correct unit of re-home) and the scope decision to ship the bounded correctness core first.
