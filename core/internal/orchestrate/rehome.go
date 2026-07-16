package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/types"
)

// RehomeSourceWorkflow is the durable, compensating driver for a fenced
// cross-Cell Source re-home (ADR-0044 slice 7). It runs on the SOURCE Cell (the
// Source's current home) and moves the Source — and thus its Entities' residency
// — to a destination Cell WITHOUT ever permitting two writers:
//
//  1. SEAL (local): fence the Source (rehoming_to=dest, epoch++). The DB seal
//     fence (enforce_write_path) now rejects this Cell's Normalizer projections of
//     the Source — a single-writer DB constraint, not just protocol. Audited; a
//     §1.8 stuck-seal Finding opens.
//  2. ADOPT (forward to dest): the destination Cell claims the Source; its
//     already-deployed Connector re-projects the Entities natively (rebuildable —
//     never shipped rows). This is the POINT OF NO RETURN (charter-guardian
//     must-fix 4): the Temporal history is the ordering authority, and there is no
//     cross-Postgres CAS to "un-adopt".
//  3. COMPLETE (local): tombstone the Source's now-unobserved Entities, resolve
//     their Findings as 'entity-rehomed', drop the Source row, resolve the
//     stuck-seal Finding.
//
// A failure BEFORE adopt commits compensates with ABORT (un-seal, epoch++ so a
// stale late adopt is fenced). A single-Cell 'local' estate has no peer Cells, so
// re-home is inapplicable and CheckRehomeGrant/seal never reach a peer.
func RehomeSourceWorkflow(ctx workflow.Context, in RehomeInput) (RehomeOutcome, error) {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities
	out := RehomeOutcome{Source: in.SourceName, DestCell: in.DestCell}

	// Authz up front (deny-by-default, §1.6): the caller must hold `rehome` on the
	// destination Cell. A denial fails before the Source is sealed.
	if err := workflow.ExecuteActivity(ctx, a.CheckRehomeGrant, in).Get(ctx, nil); err != nil {
		return out, err
	}

	// 1. Seal (fence-out). After this commits, the source Cell physically cannot
	// project the Source (DB seal fence).
	var seal SealResult
	if err := workflow.ExecuteActivity(ctx, a.SealSource, in).Get(ctx, &seal); err != nil {
		return out, err
	}

	// 2. Adopt on the destination. A failure here is PRE-commit for the move as a
	// whole (the source Cell still owns the tombstone), so compensate by aborting
	// the seal — un-freeze the Source on its original Cell (must-fix 4).
	if err := workflow.ExecuteActivity(ctx, a.ForwardAdopt, ForwardAdoptArg{
		DestCell: in.DestCell, Source: seal.Source, Epoch: seal.Epoch, Principal: in.Principal,
	}).Get(ctx, nil); err != nil {
		// Best-effort compensation on a disconnected context (the move failed; the
		// abort must run even if the workflow ctx is being torn down).
		dctx, dcancel := workflow.NewDisconnectedContext(ctx)
		defer dcancel()
		dctx = workflow.WithActivityOptions(dctx, opts)
		_ = workflow.ExecuteActivity(dctx, a.AbortSourceRehome, in).Get(dctx, nil)
		return out, fmt.Errorf("rehome %s → %s: adopt failed, seal aborted: %w", in.SourceName, in.DestCell, err)
	}

	// 3. Complete (roll-forward-only from here): tombstone the old Cell's Entities.
	var tombstoned int
	if err := workflow.ExecuteActivity(ctx, a.CompleteSourceRehome, in).Get(ctx, &tombstoned); err != nil {
		// Adopt already committed; do NOT abort. Surface the failure — the stuck
		// Finding stays open, and Complete is idempotent so a retry finishes it.
		return out, fmt.Errorf("rehome %s → %s: adopted but complete failed (retryable): %w", in.SourceName, in.DestCell, err)
	}
	out.Tombstoned = tombstoned
	return out, nil
}

// RehomeInput drives a single-Source re-home.
type RehomeInput struct {
	SourceName string
	DestCell   string
	Principal  string
}

// SealResult is the sealed Source snapshot the destination adopts (CredentialRef
// NAME only, never material — §2.5) plus its fencing epoch.
type SealResult struct {
	Source types.Source
	Epoch  int64
}

// ForwardAdoptArg is the input to ForwardAdopt.
type ForwardAdoptArg struct {
	DestCell  string
	Source    types.Source
	Epoch     int64
	Principal string
}

// RehomeOutcome is the workflow result.
type RehomeOutcome struct {
	Source     string
	DestCell   string
	Tombstoned int
}

// CheckRehomeGrant enforces the `rehome` grant on the destination Cell (§2.5,
// deny-by-default) and that the destination is a real, declared peer Cell — a
// single-Cell 'local' estate has no peers, so re-home is inapplicable and fails
// loudly rather than silently no-op'ing.
func (a *Activities) CheckRehomeGrant(ctx context.Context, in RehomeInput) error {
	if in.Principal == "" {
		a.audit(ctx, "", types.AuditRehome, "source:"+in.SourceName, types.AuditDenied)
		return temporal.NewNonRetryableApplicationError(
			"re-home requires an authenticated principal with rehome on the destination cell", "RehomeDenied", nil)
	}
	if in.DestCell == "" || in.DestCell == types.LocalCell {
		return temporal.NewNonRetryableApplicationError(
			"re-home destination must be a named peer Cell (a single-Cell 'local' estate cannot re-home)", "RehomeNoDest", nil)
	}
	peers, err := a.Store.PeerCells(ctx)
	if err != nil {
		return err
	}
	known := false
	for _, p := range peers {
		if p.Name == in.DestCell {
			known = true
			break
		}
	}
	if !known {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("re-home destination cell %q is not a declared peer", in.DestCell), "RehomeUnknownDest", nil)
	}
	allowed, err := a.Authz.Check(ctx, in.Principal, authz.RelationRehome, authz.CellObject(in.DestCell))
	if err != nil {
		return err
	}
	if !allowed {
		a.audit(ctx, in.Principal, types.AuditRehome, "source:"+in.SourceName, types.AuditDenied)
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("principal %s lacks rehome on cell:%s", in.Principal, in.DestCell), "RehomeDenied", nil)
	}
	return nil
}

// SealSource fences the Source (phase 1) and opens the §1.8 stuck-seal Finding.
func (a *Activities) SealSource(ctx context.Context, in RehomeInput) (SealResult, error) {
	src, err := a.Store.SealSourceForRehome(ctx, in.SourceName, in.DestCell)
	if err != nil {
		return SealResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "RehomeSealFailed", err)
	}
	detail, _ := json.Marshal(map[string]any{"phase": types.RehomeSealed, "destCell": in.DestCell, "epoch": src.HomeEpoch})
	if ferr := a.Store.WriteRehomeStuckFinding(ctx, in.SourceName, in.DestCell, "warning", detail); ferr != nil {
		return SealResult{}, ferr
	}
	a.rehomeAudit(ctx, in.Principal, in.SourceName, types.RehomeSealed, in.DestCell, src.HomeEpoch)
	return SealResult{Source: src, Epoch: src.HomeEpoch}, nil
}

// ForwardAdopt forwards the adopt to the destination Cell's control API (phase 2)
// over the slice-5 HMAC-signed, Principal-asserted PeerClient. The destination
// re-checks the `rehome` grant against the global OpenFGA before claiming the
// Source (§1.6). The snapshot carries a CredentialRef NAME only (§2.5) — the
// destination resolves it against its own Secrets when its Connector syncs.
func (a *Activities) ForwardAdopt(ctx context.Context, arg ForwardAdoptArg) error {
	if a.Peers == nil {
		return temporal.NewNonRetryableApplicationError(
			"cross-Cell re-home reached a Cell with no peer client configured", "NoPeerClient", nil)
	}
	endpoint, err := a.peerEndpoint(ctx, arg.DestCell)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"source": arg.Source, "epoch": arg.Epoch,
	})
	if err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), "AdoptBodyInvalid", err)
	}
	status, respBody, err := a.Peers.Post(ctx, endpoint, "/sources/rehome-adopt", body, arg.Principal, authz.KindHuman)
	if err != nil {
		return fmt.Errorf("forward adopt to cell %s: %w", arg.DestCell, err) // retryable transport error
	}
	if status < 200 || status >= 300 {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("cell %s rejected adopt: HTTP %d: %s", arg.DestCell, status, string(respBody)), "AdoptRejected", nil)
	}
	return nil
}

// CompleteSourceRehome retires the Source on its old Cell (phase 3): tombstone
// the now-unobserved Entities, resolve the stuck-seal Finding, audit complete.
func (a *Activities) CompleteSourceRehome(ctx context.Context, in RehomeInput) (int, error) {
	n, err := a.Store.CompleteRehome(ctx, in.SourceName)
	if err != nil {
		return 0, err
	}
	if err := a.Store.ResolveRehomeFinding(ctx, in.SourceName, types.RehomeComplete); err != nil {
		return 0, err
	}
	a.rehomeAudit(ctx, in.Principal, in.SourceName, types.RehomeComplete, in.DestCell, 0)
	return int(n), nil
}

// AbortSourceRehome un-seals a Source whose adopt failed pre-commit (compensation)
// and resolves the stuck-seal Finding. Best-effort + idempotent.
func (a *Activities) AbortSourceRehome(ctx context.Context, in RehomeInput) error {
	if err := a.Store.AbortRehome(ctx, in.SourceName); err != nil {
		return err
	}
	if err := a.Store.ResolveRehomeFinding(ctx, in.SourceName, types.RehomeAborted); err != nil {
		return err
	}
	a.rehomeAudit(ctx, in.Principal, in.SourceName, types.RehomeAborted, in.DestCell, 0)
	return nil
}

// peerEndpoint resolves a declared peer Cell's control endpoint.
func (a *Activities) peerEndpoint(ctx context.Context, cell string) (string, error) {
	peers, err := a.Store.PeerCells(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range peers {
		if p.Name == cell {
			return p.Endpoint, nil
		}
	}
	return "", temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("re-home destination cell %q is not a declared peer", cell), "RehomeUnknownDest", nil)
}

// rehomeAudit records one re-home phase on THIS Cell's per-Cell hash chain
// (§1.8 — seal/complete/abort on the source Cell, adopt on the destination). The
// phase + destination + epoch ride the audit detail (never secret material, §2.5).
func (a *Activities) rehomeAudit(ctx context.Context, principal, source, phase, destCell string, epoch int64) {
	if a.Store == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{"phase": phase, "destCell": destCell, "epoch": epoch})
	if err := a.Store.RecordAudit(context.WithoutCancel(ctx), types.AuditEvent{
		PrincipalID: principal, Action: types.AuditRehome, Object: "source:" + source,
		Outcome: types.AuditOK, Detail: detail, Cell: a.Store.Cell(),
	}); err != nil {
		// A failed audit write must be visible, never swallowed (§1.8).
		slog.Error("audit record failed", "action", types.AuditRehome, "phase", phase, "source", source, "err", err)
	}
}
