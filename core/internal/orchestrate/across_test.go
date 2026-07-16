package orchestrate

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/types"
)

// TestAggregateAcross covers the cross-Cell terminal-status decision (§1.8): a
// Run that skipped a region is never a silent green.
func TestAggregateAcross(t *testing.T) {
	cases := []struct {
		name     string
		children []ChildResult
		want     types.RunStatus
		touched  []string
		failed   []string
	}{
		{
			name:     "all cells succeeded",
			children: []ChildResult{{Cell: "eu", Status: types.RunSucceeded, Targets: 3}, {Cell: "us", Status: types.RunSucceeded, Targets: 2}},
			want:     types.RunSucceeded, touched: []string{"eu", "us"},
		},
		{
			name:     "some failed → partial, failed cell named",
			children: []ChildResult{{Cell: "eu", Status: types.RunSucceeded, Targets: 3}, {Cell: "us", Status: types.RunFailed, Unreachable: true}},
			want:     types.RunPartial, touched: []string{"eu", "us"}, failed: []string{"us"},
		},
		{
			name:     "all failed → failed",
			children: []ChildResult{{Cell: "eu", Status: types.RunFailed}, {Cell: "us", Status: types.RunFailed}},
			want:     types.RunFailed, touched: []string{"eu", "us"}, failed: []string{"eu", "us"},
		},
		{
			name:     "succeeded but zero targets fleet-wide → failed (matched nothing)",
			children: []ChildResult{{Cell: "eu", Status: types.RunSucceeded, Targets: 0}, {Cell: "us", Status: types.RunSucceeded, Targets: 0}},
			want:     types.RunFailed, touched: nil,
		},
		{
			name:     "one cell has targets, the empty one is not touched",
			children: []ChildResult{{Cell: "eu", Status: types.RunSucceeded, Targets: 5}, {Cell: "us", Status: types.RunSucceeded, Targets: 0}},
			want:     types.RunSucceeded, touched: []string{"eu"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, touched, failed, _ := aggregateAcross(c.children)
			if status != c.want {
				t.Fatalf("status: got %s want %s", status, c.want)
			}
			if !eqStrs(touched, c.touched) {
				t.Fatalf("touched: got %v want %v", touched, c.touched)
			}
			if !eqStrs(failed, c.failed) {
				t.Fatalf("failedCells: got %v want %v", failed, c.failed)
			}
		})
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// acrossEnv stands up the Temporal test env for RunAcrossCells with the local
// child workflow (RunAgainstView) and every activity mocked. It captures the
// FinishRunAcross argument so a test can assert the terminal status and union.
func acrossEnv(t *testing.T, fleet Fleet, localErr error, remote map[string]ChildResult) (*testsuite.TestWorkflowEnvironment, *FinishAcrossArg) {
	t.Helper()
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RunAcrossCells)
	env.RegisterWorkflow(RunAgainstView)

	var a *Activities
	env.OnActivity(a.CheckExecutionGrant, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.ListPeerCells, mock.Anything).Return(fleet, nil)
	env.OnActivity(a.MarkRunning, mock.Anything, mock.Anything).Return(nil)

	// The local child is RunAgainstView; mock ITS activities to a single-target
	// success (or a failure) without touching a real substrate.
	if localErr != nil {
		env.OnActivity(a.CheckExecutionGrant, mock.Anything, mock.Anything).Return(localErr)
	}
	env.OnActivity(a.ResolveTargetsBySite, mock.Anything, mock.Anything).Return(
		RoutedTargets{ViewVersion: 1, Groups: []SiteGroup{{Site: types.LocalSite, Targets: []actuators.Target{{EntityID: "e1", Name: "t1"}}}}}, nil)
	env.OnActivity(a.ResolveCredentials, mock.Anything, mock.Anything).Return([]dispatch.CredentialMount(nil), nil)
	env.OnActivity(a.Execute, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		dispatch.Result{Succeeded: true, PerTarget: map[string]string{"t1": "ok"}}, nil)
	env.OnActivity(a.CollectFacts, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(FactSet{}, nil)
	env.OnActivity(a.ProjectFacts, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.FinishRun, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.EnsureRun, mock.Anything, mock.Anything, mock.Anything).Return("local-child", nil)

	// Each peer's child Run is the ForwardChildRun activity — mock per cell.
	for _, p := range fleet.Peers {
		res := remote[p.Name]
		res.Cell = p.Name
		env.OnActivity(a.ForwardChildRun, mock.Anything, mock.MatchedBy(func(arg ForwardArg) bool {
			return arg.Peer.Name == p.Name
		})).Return(res, nil)
	}

	var captured FinishAcrossArg
	env.OnActivity(a.FinishRunAcross, mock.Anything, mock.MatchedBy(func(arg FinishAcrossArg) bool {
		captured = arg
		return true
	})).Return(nil)

	return env, &captured
}

// TestRunAcrossCellsPartial proves a healthy local Cell + a failed peer yields a
// PARTIAL parent Run naming the failed Cell (§1.8) and the touched-Cell union.
func TestRunAcrossCellsPartial(t *testing.T) {
	fleet := Fleet{Local: "local", Peers: []CellChild{{Name: "eu", Endpoint: "http://eu"}}}
	env, captured := acrossEnv(t, fleet, nil, map[string]ChildResult{
		"eu": {Status: types.RunFailed, Unreachable: true},
	})
	env.ExecuteWorkflow(RunAcrossCells, RunInput{RunID: "parent-1", ViewName: "v1", Principal: "alice"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}
	if captured.Status != types.RunPartial {
		t.Fatalf("status: got %s want partial", captured.Status)
	}
	if !eqStrs(captured.FailedCells, []string{"eu"}) {
		t.Fatalf("failedCells: got %v want [eu]", captured.FailedCells)
	}
	if !eqStrs(captured.Cells, []string{"eu", "local"}) {
		t.Fatalf("touched cells: got %v want [eu local]", captured.Cells)
	}
}

// TestRunAcrossCellsAllSucceed proves every Cell succeeding yields a succeeded
// parent whose Cells union names every participating Cell.
func TestRunAcrossCellsAllSucceed(t *testing.T) {
	fleet := Fleet{Local: "local", Peers: []CellChild{{Name: "eu", Endpoint: "http://eu"}}}
	env, captured := acrossEnv(t, fleet, nil, map[string]ChildResult{
		"eu": {Status: types.RunSucceeded, Targets: 4},
	})
	env.ExecuteWorkflow(RunAcrossCells, RunInput{RunID: "parent-2", ViewName: "v1", Principal: "alice"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}
	if captured.Status != types.RunSucceeded {
		t.Fatalf("status: got %s want succeeded", captured.Status)
	}
	if !eqStrs(captured.Cells, []string{"eu", "local"}) {
		t.Fatalf("touched cells: got %v want [eu local]", captured.Cells)
	}
}
