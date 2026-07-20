package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/types"
)

func TestStepEligible(t *testing.T) {
	step := func(when string, needs ...string) types.Step {
		return types.Step{Name: "s", Needs: needs, When: when}
	}
	cases := []struct {
		name       string
		step       types.Step
		state      map[string]string
		ready, met bool
	}{
		{"no needs is ready+met", step(""), map[string]string{}, true, true},
		{"pending need blocks", step("", "a"), map[string]string{"a": ""}, false, false},
		{"running need blocks", step("", "a"), map[string]string{"a": "running"}, false, false},
		{"success default met", step("", "a", "b"), map[string]string{"a": stepSucceeded, "b": stepSucceeded}, true, true},
		{"success unmet on failure", step("", "a", "b"), map[string]string{"a": stepSucceeded, "b": stepFailed}, true, false},
		{"success unmet on skip", step("", "a"), map[string]string{"a": stepSkipped}, true, false},
		{"failure met", step(types.WhenFailure, "a"), map[string]string{"a": stepFailed}, true, true},
		{"failure unmet on success", step(types.WhenFailure, "a"), map[string]string{"a": stepSucceeded}, true, false},
		{"failure unmet on skip", step(types.WhenFailure, "a"), map[string]string{"a": stepSkipped}, true, false},
		{"always met on failure", step(types.WhenAlways, "a"), map[string]string{"a": stepFailed}, true, true},
		{"always met on skip", step(types.WhenAlways, "a"), map[string]string{"a": stepSkipped}, true, true},
	}
	for _, c := range cases {
		ready, met := stepEligible(c.step, c.state)
		if ready != c.ready || met != c.met {
			t.Errorf("%s: got ready=%v met=%v, want ready=%v met=%v", c.name, ready, met, c.ready, c.met)
		}
	}
}

// ── RunDAG through the Temporal test environment ─────────────────────────────
// Activities and the child Run workflow are mocked; what's under test is the
// DAG walk itself: ordering, gate signal handling, edge conditions, and the
// terminal status. The dev-harness e2e covers the real substrate path.

func dagTestEnv(t *testing.T, spec types.Workflow, childStatus map[string]error) (*testsuite.TestWorkflowEnvironment, *map[string]string, *types.RunStatus) {
	t.Helper()
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RunDAG)
	env.RegisterWorkflow(RunAgainstView)

	var a *Activities
	env.OnActivity(a.LoadWorkflow, mock.Anything, spec.Name).Return(spec, nil)
	env.OnActivity(a.MarkWorkflowRunRunning, mock.Anything, "wr-1").Return(nil)
	env.OnActivity(a.CreateGateRecord, mock.Anything, "wr-1", mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, _, step, planDigest string, approvers types.GateApprovers) (types.Gate, error) {
			return types.Gate{ID: "gate-" + step, WorkflowRunID: "wr-1", Step: step, Status: types.GatePending, Approvers: approvers, PlanDigest: planDigest}, nil
		})
	env.OnActivity(a.RecordGateDecision, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	// Step params are resolved (event binding + re-validation) in an activity
	// before each child Run (ADR-0024); stub it to a passthrough.
	env.OnActivity(a.ResolveStepParams, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		json.RawMessage(`{}`), nil)
	// The policy activity delegates to the REAL evaluator, so a policy Step's
	// DAG behaviour is driven by its actual controls (ADR-0063).
	env.OnActivity(a.EvaluatePolicy, mock.Anything, mock.Anything).Return(
		func(_ context.Context, arg PolicyEvalArg) (types.Decision, error) {
			return policy.Evaluate(arg.Controls, arg.Context), nil
		})

	// Child Runs are stubbed per-step through OnWorkflow.
	env.OnWorkflow(RunAgainstView, mock.Anything, mock.Anything).Return(
		func(_ workflow.Context, in RunInput) (RunOutcome, error) {
			return RunOutcome{RunID: "run-" + in.StepName}, childStatus[in.StepName]
		})

	final := map[string]string{}
	finalStatus := types.RunStatus("")
	env.OnActivity(a.FinishWorkflowRun, mock.Anything, "wr-1", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ string, status types.RunStatus, steps map[string]string) error {
			finalStatus = status
			for k, v := range steps {
				final[k] = v
			}
			return nil
		})
	return env, &final, &finalStatus
}

// TestRunAgainstViewFanOutBySite proves the per-Site fan-out (ADR-0032): a View
// whose targets straddle two loci dispatches one Execute branch per (Site,
// slice) with GLOBALLY-UNIQUE slice indices (so cross-Site events never
// dedup-erase each other), and the Run records the union of Sites touched.
func TestRunAgainstViewFanOutBySite(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RunAgainstView)
	var a *Activities

	// Two groups, returned sorted by Site name: "edge-west" before "local".
	routed := RoutedTargets{ViewVersion: 1, Groups: []SiteGroup{
		{Site: "edge-west", Targets: []actuators.Target{{EntityID: "e3", Name: "t3"}}},
		{Site: types.LocalSite, Targets: []actuators.Target{{EntityID: "e1", Name: "t1"}, {EntityID: "e2", Name: "t2"}}},
	}}
	env.OnActivity(a.CheckExecutionGrant, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.ResolveTargetsBySite, mock.Anything, mock.Anything).Return(routed, nil)
	env.OnActivity(a.MarkRunning, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.ResolveCredentials, mock.Anything, mock.Anything).Return([]dispatch.CredentialMount(nil), nil)

	var mu sync.Mutex
	slicesBySite := map[string][]int{}
	env.OnActivity(a.Execute, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ RunInput, slice int, site string, resolved ResolvedTargets, _ []dispatch.CredentialMount) (dispatch.Result, error) {
			mu.Lock()
			slicesBySite[site] = append(slicesBySite[site], slice)
			mu.Unlock()
			res := dispatch.Result{Succeeded: true, PerTarget: map[string]string{}, SiteByTarget: map[string]string{}}
			for _, tgt := range resolved.Targets {
				res.PerTarget[tgt.Name] = actuators.StatusOK
				res.SiteByTarget[tgt.Name] = site
			}
			return res, nil
		})
	env.OnActivity(a.CollectFacts, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(FactSet{}, nil)
	env.OnActivity(a.ProjectFacts, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	var gotSites []string
	env.OnActivity(a.FinishRun, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ RunInput, status types.RunStatus, result dispatch.Result) error {
			gotSites = sitesTouched(result)
			return nil
		})

	env.ExecuteWorkflow(RunAgainstView, RunInput{RunID: "r1", ViewName: "v", Principal: "alice"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}

	// One branch per group (Slices unset ⇒ 1 chunk each): edge-west then local.
	if len(slicesBySite["edge-west"]) != 1 || len(slicesBySite[types.LocalSite]) != 1 {
		t.Fatalf("expected one branch per site, got %v", slicesBySite)
	}
	// Slice indices are GLOBAL and unique across sites — the collision guard.
	all := append(append([]int{}, slicesBySite["edge-west"]...), slicesBySite[types.LocalSite]...)
	sort.Ints(all)
	if !reflect.DeepEqual(all, []int{0, 1}) {
		t.Fatalf("slice indices must be globally unique {0,1}, got %v", all)
	}
	// The Run records the union of loci touched.
	if !reflect.DeepEqual(gotSites, []string{"edge-west", "local"}) {
		t.Fatalf("run sites: got %v want [edge-west local]", gotSites)
	}
}

// TestRunAgainstViewFailedTargetPropagates pins §1.8: when a Run's targets FAIL,
// RunAgainstView must both record the Run failed AND return an error to its parent
// — otherwise the parent Step (runActuationStep) folds to succeeded and a green
// Workflow hides a red Run (the exact one-click-descent trust violation the charter
// forbids). Regression guard for the live-e2e finding: ansible rc=2 on both hosts
// had left graph.run=failed but workflow_run=succeeded.
func TestRunAgainstViewFailedTargetPropagates(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RunAgainstView)
	var a *Activities

	routed := RoutedTargets{ViewVersion: 1, Groups: []SiteGroup{
		{Site: types.LocalSite, Targets: []actuators.Target{{EntityID: "e1", Name: "t1"}, {EntityID: "e2", Name: "t2"}}},
	}}
	env.OnActivity(a.CheckExecutionGrant, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.ResolveTargetsBySite, mock.Anything, mock.Anything).Return(routed, nil)
	env.OnActivity(a.MarkRunning, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.ResolveCredentials, mock.Anything, mock.Anything).Return([]dispatch.CredentialMount(nil), nil)

	// Every target fails (the ansible rc=2 shape) — Succeeded folds false.
	env.OnActivity(a.Execute, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ RunInput, _ int, _ string, resolved ResolvedTargets, _ []dispatch.CredentialMount) (dispatch.Result, error) {
			res := dispatch.Result{Succeeded: false, PerTarget: map[string]string{}}
			for _, tgt := range resolved.Targets {
				res.PerTarget[tgt.Name] = actuators.StatusFailed
			}
			return res, nil
		})
	env.OnActivity(a.CollectFacts, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(FactSet{}, nil)
	env.OnActivity(a.ProjectFacts, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	var finishStatus types.RunStatus
	env.OnActivity(a.FinishRun, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ RunInput, status types.RunStatus, _ dispatch.Result) error {
			finishStatus = status
			return nil
		})

	env.ExecuteWorkflow(RunAgainstView, RunInput{RunID: "r1", ViewName: "v", Principal: "alice"})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	// The Run row folds failed AND the child returns an error (so the parent Step fails).
	if finishStatus != types.RunFailed {
		t.Fatalf("Run must be recorded failed, got %q", finishStatus)
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("§1.8: a failed-target Run must return an error so the parent Workflow cannot report succeeded over a failed Run")
	}
}

func TestRunDAGApprovedPath(t *testing.T) {
	spec := types.Workflow{Name: "patch", Steps: []types.Step{
		{Name: "gather", ViewName: "v"},
		{Name: "approve", Needs: []string{"gather"}, Gate: &types.GateSpec{
			Approvers: types.GateApprovers{Teams: []string{"platform"}},
		}},
		{Name: "report", Needs: []string{"approve"}, ViewName: "v"},
		{Name: "cleanup", Needs: []string{"gather", "approve"}, When: types.WhenFailure, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: true, Principal: "alice"})
	}, time.Minute)

	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "patch", Principal: "alice"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"gather": stepSucceeded, "approve": stepSucceeded, "report": stepSucceeded, "cleanup": stepSkipped}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunSucceeded {
		t.Fatalf("status: %s", *status)
	}
}

func TestRunDAGDeniedRunsCleanup(t *testing.T) {
	spec := types.Workflow{Name: "patch", Steps: []types.Step{
		{Name: "gather", ViewName: "v"},
		{Name: "approve", Needs: []string{"gather"}, Gate: &types.GateSpec{
			Approvers: types.GateApprovers{Principals: []string{"alice"}},
		}},
		{Name: "report", Needs: []string{"approve"}, ViewName: "v"},
		{Name: "cleanup", Needs: []string{"approve"}, When: types.WhenFailure, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: false, Principal: "alice", Note: "not now"})
	}, time.Minute)

	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "patch", Principal: "alice"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"gather": stepSucceeded, "approve": stepFailed, "report": stepSkipped, "cleanup": stepSucceeded}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunFailed {
		t.Fatalf("denied gate must fail the workflow run, got %s", *status)
	}
}

func TestRunDAGGateTimeoutExpires(t *testing.T) {
	spec := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "approve", Gate: &types.GateSpec{
			Approvers:      types.GateApprovers{Principals: []string{"alice"}},
			TimeoutSeconds: 60,
		}},
		{Name: "after", Needs: []string{"approve"}, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})
	// No signal: the gate must expire via its timer.
	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "w"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"approve": stepFailed, "after": stepSkipped}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunFailed {
		t.Fatalf("expired gate must fail, got %s", *status)
	}
}

// Quorum (ADR-0071): a Gate with threshold N proceeds only after N DISTINCT
// approvals.
func TestRunDAGGateQuorumMet(t *testing.T) {
	spec := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "approve", Gate: &types.GateSpec{Approvers: types.GateApprovers{Teams: []string{"platform"}}, Threshold: 2}},
		{Name: "after", Needs: []string{"approve"}, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: true, Principal: "alice"})
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: true, Principal: "bob"})
	}, time.Minute)
	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "w"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"approve": stepSucceeded, "after": stepSucceeded}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunSucceeded {
		t.Fatalf("two distinct approvals must meet quorum, got %s", *status)
	}
}

// A single deny short-circuits a quorum Gate regardless of threshold.
func TestRunDAGGateQuorumDenyShortCircuits(t *testing.T) {
	spec := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "approve", Gate: &types.GateSpec{Approvers: types.GateApprovers{Teams: []string{"platform"}}, Threshold: 3}},
	}}
	env, _, status := dagTestEnv(t, spec, map[string]error{})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: false, Principal: "alice", Note: "no"})
	}, time.Minute)
	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "w"})
	if *status != types.RunFailed {
		t.Fatalf("a single deny must fail a quorum gate, got %s", *status)
	}
}

// The SAME principal approving twice does not meet a threshold of 2 (distinct
// approvals); the gate expires.
func TestRunDAGGateQuorumDistinct(t *testing.T) {
	spec := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "approve", Gate: &types.GateSpec{
			Approvers: types.GateApprovers{Teams: []string{"platform"}}, Threshold: 2, TimeoutSeconds: 60}},
	}}
	env, _, status := dagTestEnv(t, spec, map[string]error{})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: true, Principal: "alice"})
		env.SignalWorkflow(GateSignalName("approve"), GateDecision{Approved: true, Principal: "alice"})
	}, 30*time.Second)
	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "w"})
	if *status != types.RunFailed {
		t.Fatalf("one distinct approver cannot meet a quorum of 2, gate must expire, got %s", *status)
	}
}

func TestRunDAGFailedStepSkipsDownstream(t *testing.T) {
	spec := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "a", ViewName: "v"},
		{Name: "b", Needs: []string{"a"}, ViewName: "v"},
		{Name: "always", Needs: []string{"a"}, When: types.WhenAlways, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{
		"a": errors.New("boom"),
	})
	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "w"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"a": stepFailed, "b": stepSkipped, "always": stepSucceeded}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunFailed {
		t.Fatalf("status: %s", *status)
	}
}
