// Package orchestrate owns Run lifecycle on Temporal (charter §3: Temporal
// owns all lifecycle). The Phase-0 Workflow is the thesis slice (§8): resolve
// a View, execute against it as a K8s Job, project the returned facts as
// Facets with Run provenance, and record the summary.
package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	mcpact "github.com/dstout-devops/stratt/core/internal/actuators/mcp"
	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// TaskQueue is the worker queue for Run Workflows.
const TaskQueue = "stratt-runs"

// RunInput starts one Run against a View. Actuator and Params are the Step
// fields (§2.3: Step = Actuator + content + params); empty Actuator means
// ansible (the Phase-0 default). Slices > 1 partitions the target set across
// that many parallel K8s Jobs.
type RunInput struct {
	// RunID is the pre-created Run summary id for API launches. Empty for
	// Trigger-started executions: the Workflow's first activity (EnsureRun)
	// creates the row itself (ADR-0010).
	RunID    string
	ViewName string
	Actuator string
	Params   json.RawMessage
	// ViewParams binds a parametrized View's {{.param.x}} placeholders at
	// launch (ADR-0024) — resolved by ResolveTargets before selection.
	ViewParams map[string]any
	Slices     int
	// Trigger names the Trigger that fired this Run; empty for manual/API
	// launches (§1.8 descent: Trigger → Run).
	Trigger string
	// Baseline names the Baseline whose cadence runs this check; empty for
	// everything else (§1.8 descent: Baseline → Run, ADR-0019).
	Baseline string
	// WorkflowRunID/StepName link a Run executing as one Step of a Workflow
	// back to its WorkflowRun (§1.8 descent: Workflow → Run); empty for
	// direct launches.
	WorkflowRunID string
	StepName      string
	// Principal is the launching identity (§2.5) — checked for `use` on
	// each CredentialRef at dispatch time and recorded for audit. Only the
	// id travels; never any material.
	Principal string
	// CredentialRefs are pointer names to project into execution pods.
	CredentialRefs []string
}

// ResolvedTargets is what the View resolves to at dispatch time; the version
// is recorded so blast radius stays auditable (§4.3).
type ResolvedTargets struct {
	ViewVersion int64
	Targets     []actuators.Target
}

// FactSet carries per-target facts keyed for projection, plus tool-declared
// Entity observations and the Step's derived outputs schema (ADR-0017).
type FactSet struct {
	// EntityFacts: entity id → facet namespace → value.
	EntityFacts map[string]map[string]json.RawMessage
	// Entities are tool-declared observations to project with Run
	// provenance (e.g. the opentofu stratt_entities output).
	Entities []actuators.EntityObservation
	// OutputsContract is the rung-2 outputs schema the tool derived, with
	// the name it registers under (empty = none).
	OutputsContract     json.RawMessage
	OutputsContractName string
	// MCPTools are an external MCP server's declared tool schemas — the
	// rung-3 pin material (ADR-0022).
	MCPTools []actuators.MCPToolDecl
	// Workspace stamps the stratt.workspace selection label (v1 binding).
	Workspace string
}

// RunOutcome is what RunAgainstView returns to a parent workflow: the Run id
// plus the per-target verdicts and drift detail a Baseline evaluation needs
// (ADR-0019). Compact by construction — facts and events never ride here.
type RunOutcome struct {
	RunID string
	// PerTarget maps target name → status (ok | changed | failed |
	// unreachable).
	PerTarget map[string]string
	// EntityByTarget resolves target names to Entity ids (View membership at
	// dispatch time).
	EntityByTarget map[string]string
	// Drift is the per-target observed-vs-expected fragments (capped,
	// redacted upstream).
	Drift map[string][]json.RawMessage
}

// RunAgainstView is the Phase-0 Workflow. Every state transition is a
// Temporal event — the descent ladder's Workflow → Run rungs (§1.8) fall out
// of its history.
func RunAgainstView(ctx workflow.Context, in RunInput) (RunOutcome, error) {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	// Trigger-started executions have no API handler to pre-create the Run
	// summary — the Workflow owns it (ADR-0010).
	if in.RunID == "" {
		wfID := workflow.GetInfo(ctx).WorkflowExecution.ID
		if err := workflow.ExecuteActivity(ctx, a.EnsureRun, in, wfID).Get(ctx, &in.RunID); err != nil {
			return RunOutcome{}, err
		}
	}

	// Cancellation (POST /runs/{id}/cancel → Temporal CancelWorkflow, ADR-0026):
	// the Workflow is the single writer of terminal Run status, so it owns the
	// canceled transition. Activities cannot run on a canceled ctx, so cleanup
	// runs on a disconnected context — delete the Job(s), then stamp canceled.
	defer func() {
		if in.RunID == "" || !errors.Is(ctx.Err(), workflow.ErrCanceled) {
			return
		}
		dctx, dcancel := workflow.NewDisconnectedContext(ctx)
		defer dcancel()
		dctx = workflow.WithActivityOptions(dctx, opts)
		_ = workflow.ExecuteActivity(dctx, a.CleanupRun, in.RunID).Get(dctx, nil)
		_ = workflow.ExecuteActivity(dctx, a.FinishRun, in, types.RunCanceled, dispatch.Result{}).Get(dctx, nil)
	}()

	var resolved ResolvedTargets
	if err := workflow.ExecuteActivity(ctx, a.ResolveTargets, in).Get(ctx, &resolved); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.MarkRunning, in.RunID).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	// Resolve credential pointers and check the Principal's `use` grant
	// before anything executes (§2.5). Metadata only — material never
	// enters workflow state.
	var creds []dispatch.CredentialMount
	if err := workflow.ExecuteActivity(ctx, a.ResolveCredentials, in).Get(ctx, &creds); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	// Fan the target set out across slices — parallel Jobs, each an
	// independently-retryable activity whose rung shows in the Workflow
	// history (§1.8). Targets are disjoint, so results merge by union.
	chunks := splitTargets(resolved.Targets, in.Slices)
	futures := make([]workflow.Future, len(chunks))
	for i, chunk := range chunks {
		futures[i] = workflow.ExecuteActivity(ctx, a.Execute, in, i,
			ResolvedTargets{ViewVersion: resolved.ViewVersion, Targets: chunk}, creds)
	}
	var result dispatch.Result
	sliceResults := make([]dispatch.Result, len(chunks))
	var execErr error
	for i, f := range futures {
		if err := f.Get(ctx, &sliceResults[i]); err != nil && execErr == nil {
			execErr = err
		}
	}
	if execErr != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, execErr)
	}
	result = mergeResults(sliceResults)

	var facts FactSet
	if err := workflow.ExecuteActivity(ctx, a.CollectFacts, in, resolved, result).Get(ctx, &facts); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.ProjectFacts, in.RunID, facts).Get(ctx, nil); err != nil {
		return RunOutcome{RunID: in.RunID}, finishRun(ctx, a, in, types.RunFailed, err)
	}

	status := types.RunSucceeded
	if !result.Succeeded {
		status = types.RunFailed
	}
	var summaryErr error
	if err := workflow.ExecuteActivity(ctx, a.FinishRun, in, status, result).Get(ctx, nil); err != nil {
		summaryErr = err
	}
	outcome := RunOutcome{RunID: in.RunID, PerTarget: result.PerTarget, Drift: result.Drift,
		EntityByTarget: map[string]string{}}
	for _, t := range resolved.Targets {
		outcome.EntityByTarget[t.Name] = t.EntityID
	}
	return outcome, summaryErr
}

func finishRun(ctx workflow.Context, a *Activities, in RunInput, status types.RunStatus, cause error) error {
	_ = workflow.ExecuteActivity(ctx, a.FinishRun, in, status, dispatch.Result{}).Get(ctx, nil)
	return cause
}

// Activities carries the worker-side dependencies.
type Activities struct {
	Store      *graph.Store
	Dispatcher *dispatch.Dispatcher
	Bus        *events.Bus
	Authz      authz.Authorizer
	// Actuators is the registry of in-tree Actuators by name (§2.3).
	Actuators map[string]actuators.Actuator
}

// EnsureRun creates the Run summary row for a Trigger-started execution
// (ADR-0010): API launches pre-create theirs in the handler; schedule-fired
// Workflows start with only the declaration's launch parameters. Returns the
// new Run id.
func (a *Activities) EnsureRun(ctx context.Context, in RunInput, workflowID string) (string, error) {
	v, err := a.Store.GetView(ctx, in.ViewName)
	if err != nil {
		return "", err
	}
	run, err := a.Store.CreateRun(ctx, types.Run{
		WorkflowID: workflowID, ViewRef: "view://" + v.Name, ViewVersion: v.Version,
		TriggeredBy: in.Trigger, Baseline: in.Baseline,
		WorkflowRunID: in.WorkflowRunID, StepName: in.StepName,
	})
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// ResolveTargets resolves the View to its live Entity set and renders
// execution targets. Phase-0 target semantics: local-connection per target
// (see ansible.GatherFactsPlay).
func (a *Activities) ResolveTargets(ctx context.Context, in RunInput) (ResolvedTargets, error) {
	v, ents, err := a.Store.ResolveView(ctx, in.ViewName, in.ViewParams, 0)
	if err != nil {
		return ResolvedTargets{}, err
	}
	if len(ents) == 0 {
		return ResolvedTargets{}, fmt.Errorf("orchestrate: view %s resolves to zero entities", in.ViewName)
	}
	out := ResolvedTargets{ViewVersion: v.Version}
	for _, e := range ents {
		name := e.Labels["vcenter.name"]
		if name == "" {
			name = e.ID
		}
		out.Targets = append(out.Targets, actuators.Target{
			EntityID: e.ID,
			Name:     name,
			Vars:     map[string]string{"ansible_connection": "local"},
		})
	}
	return out, nil
}

// MarkRunning transitions the Run summary to running.
func (a *Activities) MarkRunning(ctx context.Context, runID string) error {
	return a.Store.SetRunStatus(ctx, runID, types.RunRunning, nil)
}

// ResolveCredentials turns CredentialRef names into pod-mountable pointers,
// enforcing the launching Principal's `use` grant (§2.5, use-without-read).
// Output is pure metadata: secret coordinates and projection policy — the
// kubelet resolves material at spawn; nothing here can hold it.
func (a *Activities) ResolveCredentials(ctx context.Context, in RunInput) ([]dispatch.CredentialMount, error) {
	if len(in.CredentialRefs) == 0 {
		return nil, nil
	}
	if in.Principal == "" {
		return nil, temporal.NewNonRetryableApplicationError(
			"credentialRefs require an authenticated principal", "CredentialUseDenied", nil)
	}
	out := make([]dispatch.CredentialMount, 0, len(in.CredentialRefs))
	for _, name := range in.CredentialRefs {
		allowed, err := a.Authz.Check(ctx, in.Principal, authz.RelationUser, "credential_ref:"+name)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("principal %s lacks use on credential_ref:%s", in.Principal, name),
				"CredentialUseDenied", nil)
		}
		ref, err := a.Store.GetCredentialRef(ctx, name)
		if err != nil {
			return nil, temporal.NewNonRetryableApplicationError(err.Error(), "CredentialRefNotFound", err)
		}
		if ref.Backend != types.BackendK8sSecret {
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("credential_ref %s: backend %s not yet implemented (ADR-0009)", name, ref.Backend),
				"BackendUnimplemented", nil)
		}
		var loc struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(ref.Locator, &loc); err != nil || loc.Name == "" {
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("credential_ref %s: invalid k8s-secret locator", name), "InvalidLocator", err)
		}
		out = append(out, dispatch.CredentialMount{
			RefName:         name,
			SecretNamespace: loc.Namespace, // "" = the Job's namespace; a mismatch fails at dispatch
			SecretName:      loc.Name,
			Injection:       ref.Injection,
		})
	}
	return out, nil
}

// Execute prepares one Step slice through its Actuator, dispatches the K8s
// Job, and follows it, publishing task events under (runID, slice).
func (a *Activities) Execute(ctx context.Context, in RunInput, slice int, resolved ResolvedTargets, creds []dispatch.CredentialMount) (dispatch.Result, error) {
	name := in.Actuator
	if name == "" {
		name = "ansible"
	}
	act, ok := a.Actuators[name]
	if !ok {
		// Unknown Actuator can never succeed — fail terminally, no retries.
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("no actuator registered as %q", name), "UnknownActuator", nil)
	}
	spec, err := act.Prepare(in.Params, resolved.Targets)
	if err != nil {
		// Malformed Step params are terminal too.
		return dispatch.Result{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	// Heartbeat from the dispatch loops so Temporal can deliver cancellation
	// to a long-running Job (a canceled Run stops promptly, ADR-0026).
	res, err := a.Dispatcher.Run(ctx, in.RunID, slice, spec, act, creds,
		func() { activity.RecordHeartbeat(ctx) })
	if err != nil {
		return dispatch.Result{}, err
	}
	return *res, nil
}

// CleanupRun deletes a Run's K8s Jobs on cancellation (invoked from the
// Workflow's disconnected cleanup path). Idempotent.
func (a *Activities) CleanupRun(ctx context.Context, runID string) error {
	return a.Dispatcher.DeleteRunJobs(ctx, runID)
}

// splitTargets partitions targets into at most n contiguous, non-empty
// chunks (n is clamped to [1, len(targets)]).
func splitTargets(targets []actuators.Target, n int) [][]actuators.Target {
	if n < 1 {
		n = 1
	}
	if n > len(targets) {
		n = len(targets)
	}
	if n <= 1 {
		return [][]actuators.Target{targets}
	}
	chunks := make([][]actuators.Target, 0, n)
	base, extra := len(targets)/n, len(targets)%n
	for i, off := 0, 0; i < n; i++ {
		size := base
		if i < extra {
			size++
		}
		chunks = append(chunks, targets[off:off+size])
		off += size
	}
	return chunks
}

// mergeResults unions per-slice results (targets are disjoint by
// construction). SpawnLatency reports the slowest slice — the value the §8
// gate bounds.
func mergeResults(slices []dispatch.Result) dispatch.Result {
	out := dispatch.Result{
		Succeeded: true,
		PerTarget: map[string]string{},
		Facts:     map[string]map[string]json.RawMessage{},
	}
	for _, r := range slices {
		out.Succeeded = out.Succeeded && r.Succeeded
		for t, s := range r.PerTarget {
			out.PerTarget[t] = s
		}
		for t, f := range r.Facts {
			out.Facts[t] = f
		}
		if r.SpawnLatency > out.SpawnLatency {
			out.SpawnLatency = r.SpawnLatency
		}
		out.Entities = append(out.Entities, r.Entities...)
		if len(r.OutputsContract) > 0 {
			out.OutputsContract = r.OutputsContract
		}
		out.MCPTools = append(out.MCPTools, r.MCPTools...)
		for t, fragments := range r.Drift {
			if out.Drift == nil {
				out.Drift = map[string][]json.RawMessage{}
			}
			out.Drift[t] = fragments
		}
	}
	return out
}

// CollectFacts joins per-target facts back to Entity ids and carries the
// tool-declared observations through (ADR-0017).
func (a *Activities) CollectFacts(ctx context.Context, in RunInput, resolved ResolvedTargets, result dispatch.Result) (FactSet, error) {
	byName := map[string]string{}
	for _, t := range resolved.Targets {
		byName[t.Name] = t.EntityID
	}
	fs := FactSet{EntityFacts: map[string]map[string]json.RawMessage{}}
	for host, facets := range result.Facts {
		if id, ok := byName[host]; ok {
			fs.EntityFacts[id] = facets
		}
	}
	fs.Entities = result.Entities
	if len(result.MCPTools) > 0 {
		// Belt to the driver's own gate: pins mint ONLY from deliberate
		// register-mode Runs — never as a side effect of a call touching a
		// sibling tool (guardian on ADR-0022).
		var p struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(in.Params, &p)
		if p.Mode == "register" {
			fs.MCPTools = result.MCPTools
		}
	}
	if len(result.OutputsContract) > 0 {
		fs.OutputsContract = result.OutputsContract
		// The workspace names the derived contract and the selection label.
		var p struct {
			Workspace string `json:"workspace"`
		}
		_ = json.Unmarshal(in.Params, &p)
		if p.Workspace != "" {
			fs.Workspace = p.Workspace
			fs.OutputsContractName = "opentofu/" + p.Workspace + ".outputs"
		}
	}
	return fs, nil
}

// ProjectFacts writes gathered facts back as Facets with Run provenance —
// the projection half of the §8 slice, via the run-provenance write path
// (§1.2, §4.3) — and projects tool-declared Entity observations plus the
// derived outputs Contract (ADR-0017).
func (a *Activities) ProjectFacts(ctx context.Context, runID string, facts FactSet) error {
	p := a.Store.RunProjector()
	prov := types.Provenance{WriterKind: types.WriterRun, WriterRef: runID, At: time.Now().UTC()}
	for entityID, facets := range facts.EntityFacts {
		for ns, value := range facets {
			if err := p.UpsertFacet(ctx, prov, entityID, ns, value); err != nil {
				return err
			}
		}
	}
	for _, obs := range facts.Entities {
		labels := map[string]string{}
		for k, v := range obs.Labels {
			labels[k] = v
		}
		if facts.Workspace != "" {
			// The v1 binding surface: downstream Views select on this
			// label (ADR-0017; parametrized Views are the follow-up).
			labels["stratt.workspace"] = facts.Workspace
		}
		if _, err := p.UpsertEntities(ctx, prov, []graph.EntityUpsert{{
			Kind: obs.Kind, IdentityKeys: obs.IdentityKeys, Labels: labels,
		}}); err != nil {
			if errors.Is(err, graph.ErrIdentityConflict) {
				// Surface, never merge (§1.2) — mirror the Syncer posture.
				return temporal.NewNonRetryableApplicationError(
					fmt.Sprintf("output entity identity conflict: %v", err), "IdentityConflict", err)
			}
			return err
		}
	}
	if len(facts.OutputsContract) > 0 && facts.OutputsContractName != "" {
		sum := sha256.Sum256(facts.OutputsContract)
		if _, err := a.Store.RegisterDerivedContract(ctx, facts.OutputsContractName,
			types.RungToolDerived, hex.EncodeToString(sum[:]), facts.OutputsContract); err != nil {
			return err
		}
	}
	// Rung 3 (§2.2, ADR-0022): pin the MCP server's declared tool schemas at
	// the declaration's rev. Canonical form here must equal what the driver
	// hashed; drift within a rev is ErrContractDrift — the Run fails
	// visibly, and accepting the change is a Git act (bump rev).
	for _, t := range facts.MCPTools {
		hash, canonical, err := mcpact.CanonicalHash(t.Schema)
		if err != nil {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("mcp tool %s/%s: %v", t.Server, t.Tool, err), "InvalidToolSchema", err)
		}
		if hash != t.Hash {
			return temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("mcp tool %s/%s: driver hash %s != control-plane hash %s (canonicalization mismatch)",
					t.Server, t.Tool, t.Hash, hash), "CanonicalizationMismatch", nil)
		}
		if err := a.Store.RegisterMCPContract(ctx, mcpact.ContractName(t.Server, t.Tool), t.Rev, hash, canonical); err != nil {
			if errors.Is(err, graph.ErrContractDrift) {
				return temporal.NewNonRetryableApplicationError(err.Error(), "ContractDrift", err)
			}
			return err
		}
	}
	return nil
}

// FinishRun records the terminal status and summary counts, then publishes
// the Run-level stream-end marker — the tail's floor arrives only after
// every slice has finished (§1.8: a floor, never a premature one).
func (a *Activities) FinishRun(ctx context.Context, in RunInput, status types.RunStatus, result dispatch.Result) error {
	counts := map[string]int{}
	for _, s := range result.PerTarget {
		counts[s]++
	}
	actuator := in.Actuator
	if actuator == "" {
		actuator = "ansible"
	}
	slices := in.Slices
	if slices < 1 {
		slices = 1
	}
	summary := map[string]any{
		"actuator":       actuator, // which engine ran the Step (§1.8 diagnosis)
		"slices":         slices,
		"targets":        len(result.PerTarget),
		"ok":             counts[actuators.StatusOK],
		"changed":        counts[actuators.StatusChanged],
		"failed":         counts[actuators.StatusFailed],
		"unreachable":    counts[actuators.StatusUnreachable],
		"spawnLatencyMs": result.SpawnLatency.Milliseconds(), // slowest slice
	}
	// Audit (§1.8, §2.5): who launched, with which credential pointers —
	// names only; material has no representation anywhere in the platform.
	if in.Principal != "" {
		summary["principal"] = in.Principal
	}
	if in.Trigger != "" {
		summary["trigger"] = in.Trigger
	}
	if in.Baseline != "" {
		summary["baseline"] = in.Baseline
	}
	if len(result.Drift) > 0 {
		// The drifted targets' observed-vs-expected detail (capped at the
		// dispatch seam) — the Run-level record behind each Finding (§1.8).
		summary["drift"] = result.Drift
	}
	if in.WorkflowRunID != "" {
		summary["workflowRun"] = in.WorkflowRunID
		summary["step"] = in.StepName
	}
	if len(in.CredentialRefs) > 0 {
		summary["credentialRefs"] = in.CredentialRefs
	}
	if err := a.Store.SetRunStatus(ctx, in.RunID, status, summary); err != nil {
		return err
	}
	// Outbound Notice on terminal failure/cancel (ADR-0027) — the outbound
	// mirror of the inbound Emitter path. Notification deliveries are
	// dispatched directly (never through this activity), so a failed delivery
	// cannot loop back into a run.failed Notice. NoticeHash dedups the publish
	// across FinishRun retries.
	if kind := noticeKindForRun(status); kind != "" {
		n := types.Notice{Kind: kind, Subject: in.RunID, Payload: map[string]any{
			"status":   string(status),
			"actuator": actuator,
			"view":     in.ViewName,
			"failed":   counts[actuators.StatusFailed] + counts[actuators.StatusUnreachable],
			"targets":  len(result.PerTarget),
		}}
		if in.Trigger != "" {
			n.Payload["trigger"] = in.Trigger
		}
		if in.Baseline != "" {
			n.Payload["baseline"] = in.Baseline
		}
		if in.Principal != "" {
			n.Payload["principal"] = in.Principal
		}
		if in.WorkflowRunID != "" {
			n.Payload["workflowRun"] = in.WorkflowRunID
			n.Payload["step"] = in.StepName
		}
		if err := a.Bus.PublishNotice(ctx, n); err != nil {
			return err
		}
	}
	// MsgID (runID/0/0) dedups the marker across FinishRun retries.
	return a.Bus.Publish(ctx, types.RunEvent{RunID: in.RunID, Kind: "stream-end"})
}

// noticeKindForRun maps a terminal Run status to its outbound Notice kind
// (ADR-0027); "" for non-notifiable terminal states (succeeded).
func noticeKindForRun(s types.RunStatus) string {
	switch s {
	case types.RunFailed:
		return types.NoticeRunFailed
	case types.RunCanceled:
		return types.NoticeRunCanceled
	}
	return ""
}
