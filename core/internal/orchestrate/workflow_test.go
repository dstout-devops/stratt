package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
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
	// The launch-input chokepoint is REGISTERED FOR REAL rather than mocked (ADR-0118 D4).
	// It touches no store — only the spec and the supplied params — so a nil *Activities
	// receiver is fine, and every DAG test then walks the actual validation the production
	// path walks. Mocking it here would have made the chokepoint invisible to exactly the
	// tests that prove RunDAG's shape.
	env.RegisterActivity(a.ResolveLaunchInputs)
	// Same treatment for the change-context chokepoint (ADR-0122): registered for real so
	// every DAG test walks the actual admission — an asserted environment or an unknown
	// changeClass is refused here, and a test that mocked past it would prove nothing about
	// the seam that refuses them. With a nil Store the derivation half is a no-op; it has its
	// own unit tests over the pure function.
	env.RegisterActivity(a.ResolveChangeContext)
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

// TestRunDAGRejectsInvalidLaunchInputs proves the chokepoint is WIRED, not merely written
// (ADR-0118 D4).
//
// Slice 1b of this arc taught the lesson the hard way: a mechanism can be implemented,
// unit-tested in isolation, and still called by nothing — and the suite stays green. So this
// drives the real RunDAG with launch inputs that violate the Workflow's declared schema and
// asserts the run FAILS before any Step runs. Removing the ResolveLaunchInputs activity call
// from RunDAG fails here.
func TestRunDAGRejectsInvalidLaunchInputs(t *testing.T) {
	spec := types.Workflow{
		Name: "needs-subnet",
		Inputs: []byte(`{"type":"object","additionalProperties":false,` +
			`"required":["targetSubnet"],"properties":{"targetSubnet":{"type":"string"}}}`),
		Steps: []types.Step{{Name: "build", ViewName: "v", Actuator: "script",
			Params: map[string]any{"script": "echo {{.launch.targetSubnet}}"}}},
	}
	env, _, status := dagTestEnv(t, spec, map[string]error{"build": nil})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: spec.Name, Principal: "alice",
		// Wrong type for a declared input, and the required one is absent.
		LaunchParams: map[string]any{"targetSubnet": 42},
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("invalid launch inputs must fail the run at the chokepoint, before any Step")
	}
	if *status != types.RunFailed {
		t.Fatalf("the WorkflowRun must be recorded failed, got %q", *status)
	}
}

// TestRunDAGAppliesLaunchDefaults: the chokepoint's other half — a declared default must be
// materialized before Steps bind {{.launch.x}}, or an omitted-but-defaulted input would
// resolve to nothing.
func TestRunDAGAppliesLaunchDefaults(t *testing.T) {
	spec := types.Workflow{
		Name: "defaulted",
		Inputs: []byte(`{"type":"object","additionalProperties":false,` +
			`"properties":{"tlsPort":{"type":"integer","default":443}}}`),
		Steps: []types.Step{{Name: "build", ViewName: "v", Actuator: "script",
			Params: map[string]any{"script": "echo {{.launch.tlsPort}}"}}},
	}
	env, _, status := dagTestEnv(t, spec, map[string]error{"build": nil})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: spec.Name, Principal: "alice",
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("an omitted input with a declared default must not fail the run: %v", err)
	}
	if *status != types.RunSucceeded {
		t.Fatalf("expected success, got %q", *status)
	}
}

// TestRunDAGRejectsAnAssertedEnvironment proves the CHANGE-CONTEXT chokepoint is wired
// (ADR-0122), and it exists because removing the ResolveChangeContext call from RunDAG broke
// nothing at all: the pure validator had its own tests, the activity was registered, and the
// suite stayed green — the same inert-mechanism shape this arc keeps finding, on a governance
// seam this time.
//
// A launcher asserting an environment is the sharp case rather than a typo'd change class: on a
// prod floor, `environment: dev` would have walked past a prod freeze window, and `dev` is a
// perfectly valid environment name so no amount of typing the string would have caught it.
func TestRunDAGRejectsAnAssertedEnvironment(t *testing.T) {
	spec := types.Workflow{
		Name:  "converge",
		Steps: []types.Step{{Name: "run", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "echo hi"}}},
	}
	env, _, status := dagTestEnv(t, spec, map[string]error{"run": nil})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: spec.Name, Principal: "alice",
		Environment: "prod", // the floor's
		Context:     map[string]any{types.ChangeContextEnvironmentKey: "dev"},
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("a launcher-asserted environment must fail the run at the chokepoint, before any Step")
	}
	if *status != types.RunFailed {
		t.Fatalf("the WorkflowRun must be recorded failed, got %q", *status)
	}
}

// The same door refuses an unknown change class — the fail-OPEN half, where a Control keyed on
// the intended class silently never fires and the change proceeds (ADR-0122 D1).
func TestRunDAGRejectsAnUnknownChangeClass(t *testing.T) {
	spec := types.Workflow{
		Name:  "converge",
		Steps: []types.Step{{Name: "run", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "echo hi"}}},
	}
	env, _, status := dagTestEnv(t, spec, map[string]error{"run": nil})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: spec.Name, Principal: "alice",
		Context: map[string]any{types.ChangeContextClassKey: "emergancy"},
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("an unknown change class must fail the run rather than be coerced")
	}
	if *status != types.RunFailed {
		t.Fatalf("the WorkflowRun must be recorded failed, got %q", *status)
	}
}

// And an ordinary change context must still launch — otherwise the two tests above would be
// satisfied by a chokepoint that refused everything.
func TestRunDAGAcceptsAnOrdinaryChangeContext(t *testing.T) {
	spec := types.Workflow{
		Name:  "converge",
		Steps: []types.Step{{Name: "run", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "echo hi"}}},
	}
	env, _, status := dagTestEnv(t, spec, map[string]error{"run": nil})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: spec.Name, Principal: "alice",
		Environment: "prod",
		Context: map[string]any{
			types.ChangeContextClassKey: types.ChangeClassEmergency,
			"incident":                  "INC-42",
			"team":                      "sre",
		},
	})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("a valid change context must not fail the run: %v", err)
	}
	if *status != types.RunSucceeded {
		t.Fatalf("run status: %q", *status)
	}
}

// ── nested Workflow Steps (ADR-0139) ─────────────────────────────────────────

// A nested Step starts a CHILD RunDAG — the same chokepoint every other launcher uses — with
// WorkflowRunID empty, so the child mints its own row and resolves its own launch inputs. §1.6 is
// satisfied structurally rather than by discipline: a nested Step cannot skip input resolution,
// because the thing it starts is the thing that resolves.
//
// The child runs FOR REAL here rather than through OnWorkflow. It has to: the child IS RunDAG, so
// mocking that type would intercept the parent as well — and running it for real is the stronger
// test anyway, because the parent link is asserted where production sets it, on the DAGInput
// EnsureWorkflowRun receives.
func TestRunDAGNestedStepStartsAChildDAG(t *testing.T) {
	spec := types.Workflow{Name: "onboard", Steps: []types.Step{
		{Name: "provision", Workflow: "build", Inputs: map[string]any{"name": "web-01"}},
	}}
	env, final, status := dagTestEnv(t, spec, nil)

	var a *Activities
	env.RegisterActivity(a.ResolveNestedInputs)
	// The child declares the input the parent Step passes. Without this the child's own
	// ResolveLaunchInputs REFUSES the unknown key — which is the chokepoint doing its job, and
	// worth stating: the nested Step gets no exemption from the rule every launcher obeys.
	child := types.Workflow{Name: "build", Inputs: json.RawMessage(
		`{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string"}}}`)}
	env.OnActivity(a.LoadWorkflow, mock.Anything, "build").Return(child, nil)
	var childIn DAGInput
	env.OnActivity(a.EnsureWorkflowRun, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, in DAGInput, _ string) (string, error) { childIn = in; return "wr-2", nil })
	env.OnActivity(a.MarkWorkflowRunRunning, mock.Anything, "wr-2").Return(nil)
	env.OnActivity(a.FinishWorkflowRun, mock.Anything, "wr-2", mock.Anything, mock.Anything).Return(nil)

	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: "onboard", Principal: "alice", Environment: "prod",
	})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("nested run must complete: %v", env.GetWorkflowError())
	}
	if (*final)["provision"] != stepSucceeded || *status != types.RunSucceeded {
		t.Fatalf("a succeeding child makes the parent Step succeed: %v %v", *final, *status)
	}
	if childIn.WorkflowName != "build" {
		t.Errorf("the child must run the named Workflow, got %q", childIn.WorkflowName)
	}
	if childIn.WorkflowRunID != "" {
		t.Errorf("WorkflowRunID must be EMPTY so the child mints its own row through "+
			"EnsureWorkflowRun — pre-creating it would be a second launch path (§1.6): got %q", childIn.WorkflowRunID)
	}
	if childIn.ParentWorkflowRunID != "wr-1" || childIn.ParentStepName != "provision" {
		t.Errorf("the child must carry the parent link, or it is an orphan whose existence is only "+
			"inferable from timing (§1.8): got %q/%q", childIn.ParentWorkflowRunID, childIn.ParentStepName)
	}
	if childIn.Principal != "alice" || childIn.Environment != "prod" {
		t.Errorf("Principal and environment are INHERITED (§2.5/ADR-0122 D2): got %q/%q",
			childIn.Principal, childIn.Environment)
	}
	if childIn.LaunchParams["name"] != "web-01" {
		t.Errorf("the Step's inputs become the child's launch params: %v", childIn.LaunchParams)
	}
}

// D6b: a nested run's terminal status IS the parent Step's status, so `needs:` +
// `when: success|failure|always` keep meaning over a nested Step exactly as over any other. Left
// unstated, those edges are undefined the moment a Step is a subtree.
func TestRunDAGNestedChildFailureFailsTheParentStep(t *testing.T) {
	spec := types.Workflow{Name: "onboard", Steps: []types.Step{
		{Name: "provision", Workflow: "build", Inputs: map[string]any{"name": "web-01"}},
		{Name: "cleanup", Needs: []string{"provision"}, When: types.WhenFailure,
			ViewName: "v", Actuator: "script"},
	}}
	env, final, status := dagTestEnv(t, spec, nil)

	var a *Activities
	env.RegisterActivity(a.ResolveNestedInputs)
	env.OnActivity(a.LoadWorkflow, mock.Anything, "build").Return(
		types.Workflow{}, temporal.NewNonRetryableApplicationError("no such workflow", "WorkflowNotFound", nil))

	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "onboard", Principal: "alice"})
	if !env.IsWorkflowCompleted() {
		t.Fatal("the parent must still complete")
	}
	if (*final)["provision"] != stepFailed {
		t.Fatalf("a failed child fails the parent Step: %v", *final)
	}
	if (*final)["cleanup"] != stepSucceeded {
		t.Fatalf("`when: failure` must still fire over a nested Step — a DAG whose conditions are "+
			"undefined for one node shape is a DAG nobody can reason about: %v", *final)
	}
	if *status != types.RunPartial && *status != types.RunFailed {
		t.Fatalf("the parent run is not a success: %v", *status)
	}
}

// THE trap (ADR-0139 D2), and it is one line that is invisible until a parent is cancelled in
// anger. Temporal's default TERMINATES children when the parent closes; a terminated RunDAG never
// reaches finishWorkflowRun, so its WorkflowRun row reads `running` FOREVER and its K8s Jobs go
// unreaped — a record that lies about a run's state.
func TestNestedChildUsesRequestCancelClosePolicy(t *testing.T) {
	src, err := os.ReadFile("workflow.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "PARENT_CLOSE_POLICY_REQUEST_CANCEL") {
		t.Fatal("a nested child must start with ParentClosePolicy REQUEST_CANCEL, never the TERMINATE " +
			"default: a terminated child never writes its terminal status, so the row reads `running` forever")
	}
	if strings.Contains(string(src), "PARENT_CLOSE_POLICY_TERMINATE") {
		t.Error("TERMINATE must not appear — the whole point is that the default is wrong here")
	}
}
