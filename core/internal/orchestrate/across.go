package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/types"
)

// RunAcrossCells is the parent workflow for a Run whose View spans more than one
// home Cell (ADR-0044 slice 5). Entities are NOT replicated across Cells, so a
// parent Cell cannot centrally partition the View's targets by home Cell — it is
// structurally blind to peer-homed rows. Instead it SCATTERS: it runs the View
// locally (a child RunAgainstView over this Cell's home entities) and, in
// parallel, launches a child Run on EACH peer Cell's control API, where the View
// re-resolves to that Cell's own home subset. It awaits all, aggregates, stamps
// the touched-Cell union, and sets a partial/succeeded/failed terminal status so
// a Run that skipped a region is never a silent green (§1.8).
//
// It reuses RunAgainstView's fan-out/await SHAPE but replaces the child-launch
// primitive: the local slice is an in-cluster child workflow; a remote slice is
// an HTTP launch+poll into the peer's own (Cell-scoped) Temporal, which the local
// cluster cannot reach. A forwarded child carries StayLocal, the recursion base
// case: it never re-enters RunAcrossCells.
func RunAcrossCells(ctx workflow.Context, in RunInput) (RunOutcome, error) {
	opts := workflow.ActivityOptions{
		// A child Run can run for hours; ForwardChildRun POSTs once then polls,
		// heartbeating every few seconds — so HeartbeatTimeout (not
		// StartToCloseTimeout) is the real liveness check. The ceiling is set very
		// high so a long-but-healthy child never trips a retry that would re-POST a
		// duplicate (the only remaining double-launch risk is a launch whose
		// response was lost in flight — see ForwardChildRun; peer-side idempotency
		// dedup is a documented follow-up).
		StartToCloseTimeout: 24 * time.Hour,
		HeartbeatTimeout:    2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	// Cancellation (POST /runs/{id}/cancel → CancelWorkflow "run-<id>"): the
	// parent owns the canceled transition. The local child cancels with the
	// workflow; each ForwardChildRun activity best-effort cancels its remote
	// child on its context cancellation. Cleanup runs on a disconnected context.
	canceled := false
	defer func() {
		if !canceled {
			return
		}
		dctx, dcancel := workflow.NewDisconnectedContext(ctx)
		defer dcancel()
		dctx = workflow.WithActivityOptions(dctx, opts)
		_ = workflow.ExecuteActivity(dctx, a.FinishRunAcross, FinishAcrossArg{
			In: in, Status: types.RunCanceled,
		}).Get(dctx, nil)
	}()

	// Authz once, up front — the one chokepoint every launch path funnels
	// through (§1.6, ADR-0028). A denial fails before any child is launched.
	if err := workflow.ExecuteActivity(ctx, a.CheckExecutionGrant, in).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, a.failAcross(ctx, in, err)
	}

	// Discover the fleet deterministically: local Cell name + peers sorted by
	// name (Temporal replay forbids map-range/unsorted nondeterminism).
	var fleet Fleet
	if err := workflow.ExecuteActivity(ctx, a.ListPeerCells).Get(ctx, &fleet); err != nil {
		return RunOutcome{RunID: in.RunID}, a.failAcross(ctx, in, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.MarkRunning, in.RunID).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, a.failAcross(ctx, in, err)
	}

	// 1. LOCAL slice — a child RunAgainstView over this Cell's home entities. It
	// gets its own Run row (RunID cleared) and stays local (no recursion; a
	// zero-entity resolution here is a benign empty success).
	localIn := in
	localIn.RunID = ""
	localIn.StayLocal = true
	cwo := workflow.ChildWorkflowOptions{WorkflowID: "run-" + in.RunID + "-local"}
	localFut := workflow.ExecuteChildWorkflow(workflow.WithChildOptions(ctx, cwo), RunAgainstView, localIn)

	// 2. REMOTE slices — one child Run POST per peer, self-scoping on the peer.
	remoteFuts := make([]workflow.Future, len(fleet.Peers))
	for i, p := range fleet.Peers {
		remoteFuts[i] = workflow.ExecuteActivity(ctx, a.ForwardChildRun, ForwardArg{Peer: p, In: in})
	}

	// Await the local child, then each remote. A remote whose activity exhausted
	// its retries is NAMED unreachable, never silently dropped (§1.8).
	children := make([]ChildResult, 0, len(fleet.Peers)+1)
	var localOutcome RunOutcome
	localErr := localFut.Get(ctx, &localOutcome)
	if temporal.IsCanceledError(localErr) {
		canceled = true
		return RunOutcome{RunID: in.RunID}, localErr
	}
	localStatus := types.RunSucceeded
	if localErr != nil {
		localStatus = types.RunFailed
	}
	children = append(children, ChildResult{
		Cell: fleet.Local, RunID: localOutcome.RunID, Status: localStatus,
		Targets: len(localOutcome.PerTarget),
	})
	for i, f := range remoteFuts {
		var cr ChildResult
		if err := f.Get(ctx, &cr); err != nil {
			if temporal.IsCanceledError(err) {
				canceled = true
				return RunOutcome{RunID: in.RunID}, err
			}
			cr = ChildResult{Cell: fleet.Peers[i].Name, Status: types.RunFailed, Unreachable: true}
		}
		children = append(children, cr)
	}

	// Aggregate: succeeded (all Cells succeeded), partial (some failed), or
	// failed (none succeeded). The touched-Cell union names every Cell that ran
	// targets or failed — the blast-radius record for §1.8 descent.
	status, touched, failedCells, total := aggregateAcross(children)

	if err := workflow.ExecuteActivity(ctx, a.FinishRunAcross, FinishAcrossArg{
		In: in, Status: status, Cells: touched, FailedCells: failedCells,
		Children: children, Targets: total,
	}).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, err
	}
	// The parent carries the LOCAL child's per-target detail; each remote Cell's
	// detail lives on its own child Run, reachable by federated descent (the
	// childRuns summary + GET /runs/{id} point-federation, ADR-0044 slice 5).
	return RunOutcome{RunID: in.RunID, PerTarget: localOutcome.PerTarget, EntityByTarget: localOutcome.EntityByTarget}, nil
}

// aggregateAcross folds per-Cell child outcomes into the parent's terminal
// status, its touched-Cell union, the named failed Cells, and the total target
// count. A View that matched no entity in ANY Cell is a failure (mirroring the
// single-Cell zero-entity error), not a hollow green.
func aggregateAcross(children []ChildResult) (status types.RunStatus, touched, failedCells []string, total int) {
	succeeded, failed := 0, 0
	for _, c := range children {
		total += c.Targets
		if c.Status == types.RunSucceeded {
			succeeded++
		} else {
			failed++
			failedCells = append(failedCells, c.Cell)
		}
		if c.Targets > 0 || c.Status != types.RunSucceeded {
			touched = append(touched, c.Cell)
		}
	}
	sort.Strings(touched)
	sort.Strings(failedCells)
	switch {
	case failed == len(children):
		status = types.RunFailed
	case failed > 0:
		status = types.RunPartial
	case total == 0:
		status = types.RunFailed // the View resolved to zero entities fleet-wide
	default:
		status = types.RunSucceeded
	}
	return status, touched, failedCells, total
}

// failAcross stamps the parent Run failed with a cause — the RunAcrossCells
// analogue of finishRun for a pre-fan-out error.
func (a *Activities) failAcross(ctx workflow.Context, in RunInput, cause error) error {
	_ = workflow.ExecuteActivity(ctx, a.FinishRunAcross, FinishAcrossArg{In: in, Status: types.RunFailed}).Get(ctx, nil)
	return cause
}

// CellChild is one peer Cell to fan a child Run to (name + control endpoint).
type CellChild struct {
	Name     string
	Endpoint string
}

// Fleet is the deterministic cross-Cell topology a RunAcrossCells fans over:
// this Cell's own name plus its peers sorted by name.
type Fleet struct {
	Local string
	Peers []CellChild
}

// ChildResult is one Cell's child-Run terminal outcome, collected for
// aggregation and descent.
type ChildResult struct {
	Cell        string
	RunID       string
	Status      types.RunStatus
	Targets     int  // how many targets this Cell ran (0 = benign empty)
	Unreachable bool // the Cell's control API could not be reached at all
}

// ForwardArg is the input to ForwardChildRun.
type ForwardArg struct {
	Peer CellChild
	In   RunInput
}

// FinishAcrossArg is the input to FinishRunAcross.
type FinishAcrossArg struct {
	In          RunInput
	Status      types.RunStatus
	Cells       []string
	FailedCells []string
	Children    []ChildResult
	Targets     int
}

// ListPeerCells returns the deterministic fleet topology (ADR-0044 slice 5) —
// this Cell's name plus its peers sorted by name.
func (a *Activities) ListPeerCells(ctx context.Context) (Fleet, error) {
	peers, err := a.Store.PeerCells(ctx)
	if err != nil {
		return Fleet{}, err
	}
	out := Fleet{Local: a.Store.Cell()}
	for _, c := range peers {
		out.Peers = append(out.Peers, CellChild{Name: c.Name, Endpoint: c.Endpoint})
	}
	return out, nil
}

// ForwardChildRun launches a child Run on one peer Cell's control API and polls
// it to a terminal status, heartbeating meanwhile (ADR-0044 slice 5). It POSTs
// ONCE, then polls internally — a transient poll error keeps looping rather than
// re-POSTing, so a Temporal retry re-POSTs only when the launch itself failed
// (no child created yet). On cancellation it best-effort cancels the remote
// child so a canceled parent doesn't orphan a peer Run.
func (a *Activities) ForwardChildRun(ctx context.Context, arg ForwardArg) (ChildResult, error) {
	if a.Peers == nil {
		return ChildResult{}, temporal.NewNonRetryableApplicationError(
			"cross-Cell run reached a Cell with no peer client configured", "NoPeerClient", nil)
	}
	body, err := childRunBody(arg.In)
	if err != nil {
		return ChildResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidChildBody", err)
	}
	kind := principalKindOf(arg.In)

	// Launch. A non-2xx is a terminal peer rejection (bad params, authz) — do not
	// retry it into a second child; a transport error IS retryable (no child yet).
	// The POST happens ONCE and polling loops internally, so a Temporal retry
	// re-POSTs only when the launch itself failed (no child created). The one
	// residual double-launch — the peer created the child but its response was
	// lost in flight — awaits peer-side idempotency dedup (a follow-up; launch-
	// level dedup is already deferred, launch.go).
	status, respBody, err := a.Peers.Post(ctx, arg.Peer.Endpoint, "/runs", body, arg.In.Principal, kind)
	if err != nil {
		return ChildResult{}, fmt.Errorf("forward child run to cell %s: %w", arg.Peer.Name, err)
	}
	if status < 200 || status >= 300 {
		return ChildResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("cell %s rejected child run: HTTP %d: %s", arg.Peer.Name, status, string(respBody)), "ChildRunRejected", nil)
	}
	var launched struct {
		ID     string          `json:"id"`
		Status types.RunStatus `json:"status"`
	}
	if err := json.Unmarshal(respBody, &launched); err != nil || launched.ID == "" {
		return ChildResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("cell %s returned an unparseable launch response: %s", arg.Peer.Name, string(respBody)), "ChildRunUnparseable", err)
	}

	// Poll to terminal, heartbeating. A transient GET error is retried in-loop
	// (never re-POSTs). Cancellation best-effort cancels the remote child.
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, _, _ = a.Peers.Post(cctx, arg.Peer.Endpoint, "/runs/"+launched.ID+"/cancel", nil, arg.In.Principal, kind)
			cancel()
			return ChildResult{}, ctx.Err()
		case <-t.C:
			activity.RecordHeartbeat(ctx, launched.ID)
			s, run, ok := a.pollChild(ctx, arg.Peer, launched.ID, arg.In.Principal, kind)
			if !ok {
				continue // transient — keep polling, do not re-launch
			}
			if isTerminal(s) {
				return ChildResult{Cell: arg.Peer.Name, RunID: launched.ID, Status: s, Targets: childTargets(run)}, nil
			}
		}
	}
}

// pollChild reads one child Run's current status from its home Cell. ok=false
// on a transient error (keep polling).
func (a *Activities) pollChild(ctx context.Context, peer CellChild, runID, principal, kind string) (types.RunStatus, map[string]any, bool) {
	status, body, err := a.Peers.Get(ctx, peer.Endpoint, "/runs/"+runID, "", principal, kind)
	if err != nil || status != http.StatusOK {
		return "", nil, false
	}
	var run map[string]any
	if err := json.Unmarshal(body, &run); err != nil {
		return "", nil, false
	}
	s, _ := run["status"].(string)
	return types.RunStatus(s), run, true
}

// childTargets extracts the target count from a child Run's summary (0 if
// absent) — a benign-empty child ran zero targets.
func childTargets(run map[string]any) int {
	summary, ok := run["summary"].(map[string]any)
	if !ok {
		return 0
	}
	n, _ := summary["targets"].(float64)
	return int(n)
}

// childRunBody builds the peer StartRun request body from a RunInput, as raw
// JSON (orchestrate must not import the api package). The peer decodes it as a
// StartRun and, seeing the verified fan-out, launches it StayLocal.
func childRunBody(in RunInput) ([]byte, error) {
	body := map[string]any{"viewName": in.ViewName}
	if in.Actuator != "" {
		body["actuator"] = in.Actuator
	}
	if len(in.Params) > 0 {
		body["params"] = json.RawMessage(in.Params)
	}
	if in.Slices > 0 {
		body["slices"] = in.Slices
	}
	if len(in.CredentialRefs) > 0 {
		body["credentialRefs"] = in.CredentialRefs
	}
	return json.Marshal(body)
}

// principalKindOf reports the Principal kind to assert on the forwarded child
// launch. Only the id rides RunInput; humans are the default (the peer
// re-evaluates authz regardless, §1.6).
func principalKindOf(in RunInput) string {
	if in.Principal == "" {
		return ""
	}
	return authz.KindHuman
}

func isTerminal(s types.RunStatus) bool {
	switch s {
	case types.RunSucceeded, types.RunFailed, types.RunCanceled, types.RunPartial:
		return true
	}
	return false
}

// FinishRunAcross stamps the parent Run's terminal status, its touched-Cell
// union, and a summary naming its child Runs (for descent) and any failed Cells
// (§1.8). A failed/partial parent emits the run.failed Notice so the outbound
// path alerts on a region that didn't run (ADR-0027).
func (a *Activities) FinishRunAcross(ctx context.Context, arg FinishAcrossArg) error {
	in := arg.In
	summary := map[string]any{
		"actuator":  in.Actuator, // inherited from the parent Run — never defaulted
		"view":      in.ViewName,
		"targets":   arg.Targets,
		"crossCell": true,
	}
	if in.Principal != "" {
		summary["principal"] = in.Principal
	}
	if len(arg.Children) > 0 {
		kids := make([]map[string]any, 0, len(arg.Children))
		for _, c := range arg.Children {
			kids = append(kids, map[string]any{"cell": c.Cell, "run": c.RunID, "status": string(c.Status)})
		}
		summary["childRuns"] = kids
	}
	if len(arg.FailedCells) > 0 {
		summary["failedCells"] = arg.FailedCells
	}
	if len(arg.Cells) > 0 {
		summary["cells"] = arg.Cells
	}
	if err := a.Store.SetRunStatus(ctx, in.RunID, arg.Status, summary); err != nil {
		return err
	}
	if err := a.Store.SetRunCells(ctx, in.RunID, arg.Cells); err != nil {
		return err
	}
	// A failed OR partial cross-Cell Run alerts (a partial skipped a region —
	// operators must know, §1.8). Canceled uses the canceled Notice.
	kind := ""
	switch arg.Status {
	case types.RunFailed, types.RunPartial:
		kind = types.NoticeRunFailed
	case types.RunCanceled:
		kind = types.NoticeRunCanceled
	}
	if kind != "" && a.Bus != nil {
		n := types.Notice{Kind: kind, Subject: in.RunID, Payload: map[string]any{
			"status": string(arg.Status), "view": in.ViewName,
			"failedCells": arg.FailedCells, "cells": arg.Cells,
		}}
		if err := a.Bus.PublishNotice(ctx, n); err != nil {
			return err
		}
	}
	if a.Bus != nil {
		return a.Bus.Publish(ctx, types.RunEvent{RunID: in.RunID, Kind: "stream-end"})
	}
	return nil
}
