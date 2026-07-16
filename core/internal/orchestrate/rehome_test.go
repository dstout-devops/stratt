package orchestrate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/dstout-devops/stratt/types"
)

// TestRehomeSourceWorkflowHappyPath proves the phase ordering (ADR-0044 slice 7):
// grant → seal → adopt → complete, with NO abort on the success path.
func TestRehomeSourceWorkflowHappyPath(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RehomeSourceWorkflow)
	var a *Activities

	env.OnActivity(a.CheckRehomeGrant, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.SealSource, mock.Anything, mock.Anything).Return(
		SealResult{Source: types.Source{Name: "vc-prod", Cell: "eu"}, Epoch: 1}, nil)
	env.OnActivity(a.ForwardAdopt, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.CompleteSourceRehome, mock.Anything, mock.Anything).Return(4, nil)
	// Abort must NOT run on the happy path — assert by NOT registering it and
	// letting a stray call fail the mock expectations.

	env.ExecuteWorkflow(RehomeSourceWorkflow, RehomeInput{SourceName: "vc-prod", DestCell: "us", Principal: "alice"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	var out RehomeOutcome
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("result: %v", err)
	}
	if out.Tombstoned != 4 || out.DestCell != "us" || out.Source != "vc-prod" {
		t.Fatalf("outcome: %+v", out)
	}
	env.AssertExpectations(t)
}

// TestRehomeSourceWorkflowAdoptFailAborts proves the compensation (must-fix 4): a
// pre-commit adopt failure un-seals the Source via Abort and COMPLETE never runs.
func TestRehomeSourceWorkflowAdoptFailAborts(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RehomeSourceWorkflow)
	var a *Activities

	env.OnActivity(a.CheckRehomeGrant, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.SealSource, mock.Anything, mock.Anything).Return(
		SealResult{Source: types.Source{Name: "vc-prod", Cell: "eu"}, Epoch: 1}, nil)
	env.OnActivity(a.ForwardAdopt, mock.Anything, mock.Anything).Return(
		assertErr("destination unreachable"))
	aborted := false
	env.OnActivity(a.AbortSourceRehome, mock.Anything, mock.Anything).Return(
		func(_ context.Context, _ RehomeInput) error { aborted = true; return nil })
	// CompleteSourceRehome must NOT run — not registered; a stray call fails.

	env.ExecuteWorkflow(RehomeSourceWorkflow, RehomeInput{SourceName: "vc-prod", DestCell: "us", Principal: "alice"})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected a workflow error on adopt failure")
	}
	if !aborted {
		t.Fatal("adopt failure must trigger the compensating Abort (un-seal)")
	}
}

// TestRehomeSourceWorkflowDenyStopsSeal proves the authz chokepoint: a denied
// grant fails BEFORE the Source is sealed (no seal, no adopt).
func TestRehomeSourceWorkflowDenyStopsSeal(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(RehomeSourceWorkflow)
	var a *Activities

	env.OnActivity(a.CheckRehomeGrant, mock.Anything, mock.Anything).Return(assertErr("denied"))
	// SealSource / ForwardAdopt / Complete must NOT run.

	env.ExecuteWorkflow(RehomeSourceWorkflow, RehomeInput{SourceName: "vc-prod", DestCell: "us", Principal: "mallory"})
	if env.GetWorkflowError() == nil {
		t.Fatal("expected denial to fail the workflow")
	}
}

// TestCheckRehomeGrantGuards covers the infra-free denial guards: an
// unauthenticated caller and a 'local' (peerless) destination both fail before
// any Store/authz lookup.
func TestCheckRehomeGrantGuards(t *testing.T) {
	a := &Activities{} // no Store, no Authz — these paths must not reach them

	if err := a.CheckRehomeGrant(t.Context(), RehomeInput{SourceName: "s", DestCell: "us"}); err == nil {
		t.Fatal("empty principal must be denied")
	}
	if err := a.CheckRehomeGrant(t.Context(), RehomeInput{SourceName: "s", DestCell: types.LocalCell, Principal: "alice"}); err == nil {
		t.Fatal("a 'local' (peerless) destination must be rejected")
	}
	if err := a.CheckRehomeGrant(t.Context(), RehomeInput{SourceName: "s", DestCell: "", Principal: "alice"}); err == nil {
		t.Fatal("an empty destination must be rejected")
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }
func assertErr(s string) error { return strErr(s) }
