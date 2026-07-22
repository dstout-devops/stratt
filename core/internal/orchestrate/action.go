// This file adds the Action execution path (charter §2.2, ADR-0031): a
// targetless typed operation. It is a sibling of RunAgainstView, deliberately
// separate so the View-scoped execution chokepoint (ADR-0028) stays clean —
// Actions are not View-scoped, so their authz chokepoint is the CredentialRef
// `use`-check (§2.5), not a runner-on-View grant.
package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/types"
)

// RunAction executes one Connector Action as a Run (§2.2). No View, no target
// fan-out: EnsureActionRun → MarkRunning → ResolveCredentials (the §2.5
// use-check, the Action's authz chokepoint) → ExecuteAction (one pod) →
// ValidateActionOutputs (produced outputs vs the output Contract, skipped for
// dry-run) → RecordActionResult (capture outputs, project any Entities) →
// FinishRun. Returns the typed Outputs for cross-Step binding.
func RunAction(ctx workflow.Context, in RunInput) (RunOutcome, error) {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	if in.RunID == "" {
		wfID := workflow.GetInfo(ctx).WorkflowExecution.ID
		if err := workflow.ExecuteActivity(ctx, a.EnsureActionRun, in, wfID).Get(ctx, &in.RunID); err != nil {
			return RunOutcome{}, err
		}
	}

	// Cancellation cleanup — the Workflow is the single writer of terminal
	// status, mirroring RunAgainstView (ADR-0026).
	defer func() {
		if in.RunID == "" || !errors.Is(ctx.Err(), workflow.ErrCanceled) {
			return
		}
		dctx, dcancel := workflow.NewDisconnectedContext(ctx)
		defer dcancel()
		dctx = workflow.WithActivityOptions(dctx, opts)
		// Actions are targetless (v1 runs them on the hub), so no remote Sites
		// to cancel (ADR-0032) — pass nil.
		_ = workflow.ExecuteActivity(dctx, a.CleanupRun, in.RunID, []string(nil)).Get(dctx, nil)
		_ = workflow.ExecuteActivity(dctx, a.FinishRun, in, types.RunCanceled, dispatch.Result{}).Get(dctx, nil)
	}()

	if err := workflow.ExecuteActivity(ctx, a.MarkRunning, in.RunID).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	var creds []dispatch.CredentialMount
	if err := workflow.ExecuteActivity(ctx, a.ResolveCredentials, in).Get(ctx, &creds); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	var res dispatch.Result
	if err := workflow.ExecuteActivity(ctx, a.ExecuteAction, in, creds).Get(ctx, &res); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	// The output Contract is the Action's defining feature (§2.2). A dry-run's
	// plan is not the contracted output, so it is not validated here.
	if !in.DryRun {
		if err := workflow.ExecuteActivity(ctx, a.ValidateActionOutputs, in, res.Outputs).Get(ctx, nil); err != nil {
			return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
		}
	}

	if err := workflow.ExecuteActivity(ctx, a.RecordActionResult, in, res).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	status := types.RunSucceeded
	if !res.Succeeded {
		status = types.RunFailed
	}
	if err := workflow.ExecuteActivity(ctx, a.FinishRun, in, status, res).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, err
	}
	return RunOutcome{RunID: in.RunID, Outputs: res.Outputs}, nil
}

// EnsureActionRun creates the Run summary for a Trigger/Workflow-started Action
// (API launches pre-create theirs). Targetless — the "view" ref records the
// Action (§1.8 descent), not a graph View.
func (a *Activities) EnsureActionRun(ctx context.Context, in RunInput, workflowID string) (string, error) {
	run, err := a.Store.CreateRun(ctx, types.Run{
		WorkflowID: workflowID, ViewRef: "action://" + in.Action,
		TriggeredBy: in.Trigger, WorkflowRunID: in.WorkflowRunID, StepName: in.StepName,
	})
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// ExecuteAction prepares the Action into pod content and dispatches it (one
// pod, no targets). dryRun on a non-DryRunnable Action is terminal.
func (a *Activities) ExecuteAction(ctx context.Context, in RunInput, creds []dispatch.CredentialMount) (dispatch.Result, error) {
	// ── Shared preamble, BEFORE the pod-vs-plugin branch (§2.5/§1.6) ──────────
	// Every Action is gated: the CredentialRef use-check is the Action's ONLY
	// authz chokepoint, so a credential-free Action is refused on EITHER path.
	// ResolveCredentials already ran the use-check + audit per name; a name the
	// launching Principal lacked `use` on never reached here (a plugin Action
	// cannot escape it — an unregistered/unauthorized name fails there).
	if len(creds) == 0 {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("action %q must carry a CredentialRef — the use-check is its only authz gate until the Action run-grant lands", in.Action),
			"ActionUngated", nil)
	}

	// ── Route: a plugin-provided Action goes over the port; else the pod path ─
	if pa, ok := a.PluginActions[in.Action]; ok {
		// Dry-run is refused CORE-SIDE from the reconciled ActionDecl, never
		// delegated (a plugin that ignores dry_run would run live side effects).
		if in.DryRun && !pa.DryRunnable {
			return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("action %q does not support dry-run", in.Action), "DryRunUnsupported", nil)
		}
		// The use-checked, authorized CredentialRefs cross with their resolved Secret
		// COORDINATES (ADR-0052) — names + name/namespace/key coordinates, NEVER
		// material; the plugin's SDK SecretBroker resolves the material itself, confined
		// to these. The host attaches coordinates only on the local path (MF-C). The
		// launching Principal — not the plugin channel identity — carries the invocation.
		portCreds := make([]pluginhost.Credential, 0, len(creds))
		for _, c := range creds {
			keys := make([]pluginhost.CredentialKey, 0, len(c.Injection))
			for _, inj := range c.Injection {
				keys = append(keys, pluginhost.CredentialKey{Key: inj.Key, As: inj.As, Name: inj.Name})
			}
			portCreds = append(portCreds, pluginhost.Credential{
				RefName: c.RefName, SecretNamespace: c.SecretNamespace, SecretName: c.SecretName,
				Vault: c.Vault, // backend: vault KV coordinate (ADR-0094), nil for k8s-secret
				Keys:  keys,
			})
		}
		raw, err := pa.Host.InvokeRaw(ctx, pluginhost.ActionInvoke{
			Principal:            in.Principal,
			Action:               in.Action,
			Args:                 in.Params,
			DryRun:               in.DryRun,
			Credentials:          portCreds,
			ExpectOutputContract: "actions/" + in.Action + ".output",
		})
		if err != nil {
			return dispatch.Result{}, err
		}
		// Surface governance rejections (dropped land-grabs) as first-class §1.8
		// signals — a RunEvent + a tracked Finding, never a swallowed log line
		// (enterprise-readiness GOV-3).
		a.surfaceRejections(ctx, in.RunID, "action", in.Action, raw.Rejections)
		// Entities are GOVERNED but UNPROJECTED — RecordActionResult performs the
		// single write with RUN provenance (per-verb write path, ADR-0047 §2).
		ents := make([]actuators.EntityObservation, 0, len(raw.Entities))
		for _, e := range raw.Entities {
			rels := make([]actuators.RelationObservation, 0, len(e.Relations))
			for _, r := range e.Relations {
				rels = append(rels, actuators.RelationObservation{Type: r.Type, ToScheme: r.ToScheme, ToValue: r.ToValue})
			}
			ents = append(ents, actuators.EntityObservation{Kind: e.Kind, IdentityKeys: e.IdentityKeys, Labels: e.Labels, Relations: rels})
		}
		return dispatch.Result{Succeeded: raw.OK, Outputs: raw.Outputs, Entities: ents}, nil
	}

	// ── In-tree pod path ─────────────────────────────────────────────────────
	act, ok := a.Actions[in.Action]
	if !ok {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("no action registered as %q", in.Action), "UnknownAction", nil)
	}
	if in.DryRun && !act.DryRunnable() {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("action %q does not support dry-run", in.Action), "DryRunUnsupported", nil)
	}
	spec, err := act.Prepare(in.Params, in.DryRun)
	if err != nil {
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidActionParams", err)
	}
	res, err := a.Dispatcher.Run(ctx, in.RunID, 0, spec, act, creds,
		func() { activity.RecordHeartbeat(ctx) })
	if err != nil {
		return dispatch.Result{}, err
	}
	return *res, nil
}

// ValidateActionOutputs checks the produced outputs against the Action's output
// Contract (§2.2). A mismatch fails the Run terminally — an Action that lies
// about its outputs is a §1.8 failure, never silently accepted.
func (a *Activities) ValidateActionOutputs(ctx context.Context, in RunInput, outputs json.RawMessage) error {
	if err := contract.ValidateActionOutput(in.Action, outputs); err != nil {
		return temporal.NewNonRetryableApplicationError(err.Error(), "ActionOutputInvalid", err)
	}
	return nil
}

// RecordActionResult captures the Action's typed outputs on the Run and
// projects any tool-declared Entity observations with Run provenance
// (create-vm → an instance Entity, §1.2 — the ADR-0017 path, Action-typed).
func (a *Activities) RecordActionResult(ctx context.Context, in RunInput, res dispatch.Result) error {
	if len(res.Outputs) > 0 {
		if err := a.Store.SetRunOutputs(ctx, in.RunID, res.Outputs); err != nil {
			return err
		}
	}
	if len(res.Entities) > 0 {
		return a.ProjectFacts(ctx, in.RunID, FactSet{Entities: res.Entities})
	}
	return nil
}
