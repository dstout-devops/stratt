package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

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
	env.OnActivity(a.CreateGateRecord, mock.Anything, "wr-1", mock.Anything, mock.Anything).Return(
		func(_ context.Context, _, step string, approvers types.GateApprovers) (types.Gate, error) {
			return types.Gate{ID: "gate-" + step, WorkflowRunID: "wr-1", Step: step, Status: types.GatePending, Approvers: approvers}, nil
		})
	env.OnActivity(a.RecordGateDecision, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	// Step params are resolved (event binding + re-validation) in an activity
	// before each child Run (ADR-0024); stub it to a passthrough.
	env.OnActivity(a.ResolveStepParams, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		json.RawMessage(`{}`), nil)

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
