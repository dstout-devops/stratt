# ADR 0045 — DB-driven Syncer instantiation & Connector home-ownership gate (full re-home auto-cutover)

- **Status:** **Partially landed** (2026-07-16). The **DB home-ownership gate** + **seal-safe
  `RegisterSource`** — the single-writer correctness core (design-review must-fixes 1 & 3) — shipped as
  migration `00032_home_gate.sql`; the **full auto-cutover** (fleet resolver, `main` standby supervisor,
  home-collision + standby Findings, active/standby/sealed status API — must-fixes 2 & 4) remains
  **Proposed / scheduled**. The gate makes destination-side single-writer a DB constraint *now*; the
  redeploy-free cutover it enables is the follow-up.
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

## Landed increment (2026-07-16): the DB home gate + seal-safe register

A charter-guardian design review of the full auto-cutover returned CHANGES-REQUIRED with four must-fixes;
the two that are the single-writer **correctness core** shipped first as a bounded, independently-valuable
increment (the redeploy-free cutover they enable follows):

- **Home gate is a DB CONSTRAINT (must-fix 1), not a Go convention.** Migration `00032_home_gate.sql`
  extends `enforce_write_path`: the Normalizer projector declares its Cell as `stratt.cell`, and a Normalizer
  projection whose Source is homed on a **named peer** Cell is rejected at the data layer — folded into the
  seal fence's existing source lookup (no extra query). Fires only when both the daemon and the Source's
  home are named Cells and differ; an unclaimed / `local` Source is claim-by-projection, so a single-Cell
  `local` estate is byte-identical. This **closes the steady-state half of ADR-0044 residual tension #4**:
  destination-side single-writer no longer leans on protocol once a Source is homed. Proven on real Postgres
  (`TestHomeGateRejectsPeerHomedProjection`).
- **Seal-safe `RegisterSource` (must-fix 3).** A Connector restart on a **sealed** Source leaves the row
  completely untouched — never rewrites its home or resets its `home_epoch` mid-move (the DO UPDATE is gated
  on `rehoming_to IS NULL`). `TestRegisterSourceSealSafe`.

**Still Proposed / scheduled (the redeploy-free cutover):** the fleet **home resolver** + `GET /sources/{name}`,
the `main` **standby supervisor** (loop-gate so a standby Connector does not enumerate-then-drop the external
SoR — must-fix 6), the periodic **home-collision reconcile** raising a `critical` Finding when >1 Cell homes
one Source name (the greenfield race — must-fix 2, never a silent tiebreak, §2.4 anti-GPO), and the **standby
Finding + active/standby/sealed status** on the sources read model so a standby is never silent (must-fix 4).
Until those land, the ADR-0044 slice-7 runbook step (deploy/enable the Connector on the destination Cell)
stands, and the home gate above guarantees no double-writer regardless.

## Decision (the full auto-cutover — remaining, Proposed)

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
- **charter-guardian (DESIGN review of the full auto-cutover):** CHANGES-REQUIRED. Direction sound (strictly
  better than today's unconditional double-writer), four must-fixes: (1) the home gate must be a DB
  constraint not a Go convention — **landed** (migration 00032); (2) the greenfield simultaneous-claim race
  is a *silent* double-writer (the slice-2 placement Finding can't see it), so it needs a home-collision
  reconcile Finding, never a silent tiebreak — **scheduled**; (3) seal-safe `RegisterSource` — **landed**;
  (4) standby must never be silent (Finding + status) — **scheduled**. Should-fixes (loop-gate supervisor to
  avoid enumerate-then-drop; `GET /sources/{name}` under authz+audit+HMAC; byte-identical single-Cell —
  **honored**) captured above. The landed increment was steward-scoped as the split "harden the DB gate now,
  auto-cutover next."
- **Flag (widened residual tension):** the full auto-cutover would extend ADR-0044 residual tension #4 from
  the brief re-home window to the steady-state life of every standby Connector; the landed DB home gate
  (must-fix 1) is precisely what keeps that a DB constraint rather than protocol.
