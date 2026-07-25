package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// These cover resolveFindingLaunch — the decision behind GET/POST /findings/{id}/remediation —
// with the Baseline read injected, because Server.Store is a concrete *graph.Store and a test
// that needed one would be skipped in `task ci`. The withdrawal branch in particular exists to
// serve a Finding whose Baseline is GONE, so a test that could only run against a live store
// would be guarding the one path hardest to set up.

func noBaseline(string) (types.Baseline, error) {
	return types.Baseline{}, fmt.Errorf("%w: baseline", graph.ErrNotFound)
}

// An orphan Finding carries its own launch spec and must resolve WITHOUT reading a Baseline —
// its Baseline was pruned in the same Apply that wrote it. The injected lookup fails on purpose:
// if resolution ever consults it for a withdrawal, this test fails.
func TestOrphanFindingResolvesWithoutItsPrunedBaseline(t *testing.T) {
	f := types.Finding{
		ID: "f1", Baseline: "compiled:prod-web/web-server@1/0", Framework: "orphan",
		LaunchWorkflow: "web-retire", LaunchParams: map[string]any{"port": "8443"},
		// Kind travels WITH the workflow — migration 00042's finding_launch_spec_complete
		// constraint makes (workflow set, kind null) unstorable, so a fixture without it would
		// be testing a row the database cannot hold.
		LaunchKind: types.LaunchRemove,
	}
	fl, prob := resolveFindingLaunch(f, noBaseline)
	if prob != nil {
		t.Fatalf("an orphan must resolve from the Finding alone: %+v", prob)
	}
	if fl.Workflow != "web-retire" || fl.Params["port"] != "8443" {
		t.Fatalf("wrong launch: %+v", fl)
	}
	if fl.Kind != types.LaunchRemove {
		t.Fatalf("the act must be named: retiring abandoned state is not converging live state, and the preview says which; got %q", fl.Kind)
	}
	if fl.Baseline != f.Baseline {
		t.Fatalf("the pruned Baseline is still the right identifier for the state being retired, got %q", fl.Baseline)
	}
}

// A retained orphan (onRemove:retain) has no Baseline and no withdrawal Workflow. It must say so
// in terms of the declaration, not report a missing row — the state is deliberately kept, and
// "baseline not found" sends the operator looking for a bug instead of reading their Intent.
func TestRetainedOrphanExplainsWhyThereIsNothingToLaunch(t *testing.T) {
	f := types.Finding{ID: "f2", Baseline: "compiled:prod-web/web-server@1/0", Framework: "orphan"}
	_, prob := resolveFindingLaunch(f, noBaseline)
	if prob == nil {
		t.Fatal("a retained orphan routes to nothing")
	}
	if prob.Err != nil {
		t.Fatalf("this is a decision, not a store failure to hand to fail(): %v", prob.Err)
	}
	if prob.Status != http.StatusNotFound {
		t.Fatalf("status: got %d", prob.Status)
	}
	for _, want := range []string{"f2", "nothing to launch", "onRemove: retain", "kept deliberately"} {
		if !strings.Contains(prob.Message, want) {
			t.Errorf("the message must explain the declaration, not the missing row; want %q in: %s", want, prob.Message)
		}
	}
	// It must NOT assert which cause applies. The framework check that used to let it speak
	// confidently about withdrawal was removed (ADR-0120 V5) because it made Framework a second
	// discriminator beside LaunchKind; the honest replacement states the facts and lists causes.
	if strings.Contains(prob.Message, "reports state abandoned") {
		t.Errorf("the message must not assert a cause it cannot know: %s", prob.Message)
	}
}

// A store failure that is NOT "no such row" must reach fail(), which owns the error→status
// mapping. Only a missing Baseline is a decision this function is entitled to make; a dropped
// connection is not, and turning one into "nothing to launch" would report a substrate outage as
// a deliberate declaration.
func TestRealStoreFailureIsHandedToFail(t *testing.T) {
	f := types.Finding{ID: "f3", Baseline: "cis-1", Framework: "cis"}
	_, prob := resolveFindingLaunch(f, func(string) (types.Baseline, error) {
		return types.Baseline{}, errors.New("connection reset")
	})
	if prob == nil {
		t.Fatal("a store failure is not resolvable")
	}
	if prob.Err == nil {
		t.Fatalf("it must be handed to fail() rather than turned into a message here: %+v", prob)
	}
}

// A MISSING Baseline is a 404 decision regardless of framework — this is the observable proof
// that the Framework == "orphan" branch is gone (ADR-0120 V5). Framework carried "orphan" and
// "provision" while LaunchKind now carries the act; branching on both would let them disagree,
// with the winner decided by whichever test ran first (§2.4).
func TestMissingBaselineIsADecisionWhateverTheFramework(t *testing.T) {
	for _, framework := range []string{"", "cis", "orphan", "provision"} {
		f := types.Finding{ID: "f6", Baseline: "gone", Framework: framework}
		_, prob := resolveFindingLaunch(f, noBaseline)
		if prob == nil || prob.Err != nil || prob.Status != http.StatusNotFound {
			t.Errorf("framework %q: a missing baseline with no launch spec must be one 404 decision, got %+v", framework, prob)
		}
	}
}

// The ordinary path is unchanged: a drift Finding reads its spec from its live Baseline and is
// NOT marked a withdrawal.
func TestDriftFindingResolvesFromItsBaseline(t *testing.T) {
	f := types.Finding{ID: "f4", Baseline: "compiled:prod-web/web-server@1/0"}
	fl, prob := resolveFindingLaunch(f, func(name string) (types.Baseline, error) {
		return types.Baseline{
			Name: name, RemediationWorkflow: "web-converge",
			RemediationParams: map[string]any{"port": "8443"},
		}, nil
	})
	if prob != nil {
		t.Fatalf("unexpected problem: %+v", prob)
	}
	if fl.Workflow != "web-converge" || fl.Kind != types.LaunchRemediate {
		t.Fatalf("wrong launch: %+v", fl)
	}
}

// A Baseline that routes no remediation still 404s by name, and must not be mistaken for a
// withdrawal.
func TestBaselineWithoutRemediationRoutesToNothing(t *testing.T) {
	f := types.Finding{ID: "f5", Baseline: "observe-only"}
	_, prob := resolveFindingLaunch(f, func(name string) (types.Baseline, error) {
		return types.Baseline{Name: name}, nil
	})
	if prob == nil || prob.Status != http.StatusNotFound {
		t.Fatalf("expected a 404 decision, got %+v", prob)
	}
	if !strings.Contains(prob.Message, "observe-only") {
		t.Fatalf("the message must name the Baseline: %s", prob.Message)
	}
}

// A PROVISION Finding must resolve through the same door as the others (ADR-0120 D5). Its Baseline
// name — provision/<intent> — is synthetic and has no row at all, so like an orphan it must resolve
// without a Baseline read; the injected lookup fails on purpose to prove it never tries.
//
// This is the end of the cascade the whole arc is about: an Intent declares desired state, the
// reconcile derives which instance is missing and what to build it with, the Finding carries that
// typed, and this door hands it to a gated Workflow. Without this test the last hop is asserted by
// a comment.
func TestProvisionFindingResolvesThroughTheSameDoor(t *testing.T) {
	f := types.Finding{
		ID: "p1", Baseline: "provision/web-fleet", Target: "web-02", Framework: "provision",
		LaunchWorkflow: "compute-build", LaunchKind: types.LaunchBuild,
		LaunchParams: map[string]any{
			"instance": "web-02",
			"labels":   map[string]any{"stratt.intent/instance": "web-02"},
		},
	}
	fl, prob := resolveFindingLaunch(f, noBaseline)
	if prob != nil {
		t.Fatalf("a provision Finding must resolve from the Finding alone: %+v", prob)
	}
	if fl.Workflow != "compute-build" {
		t.Fatalf("workflow: got %q", fl.Workflow)
	}
	if fl.Kind != types.LaunchBuild {
		t.Fatalf("the act must be named build, not inferred: got %q", fl.Kind)
	}
	if fl.Params["instance"] != "web-02" {
		t.Fatalf("the per-instance identity must reach the launch: %#v", fl.Params)
	}
	// The correlation label has to survive too: the next reconcile matches on it to decide the
	// instance is built, so losing it here means a build that never resolves its own Finding.
	labels, _ := fl.Params["labels"].(map[string]any)
	if labels["stratt.intent/instance"] != "web-02" {
		t.Fatalf("the correlation label must reach the launch: %#v", fl.Params)
	}
}
