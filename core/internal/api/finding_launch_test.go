package api

import (
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
		RemoveWorkflow: "web-retire", RemoveParams: map[string]any{"port": "8443"},
	}
	fl, prob := resolveFindingLaunch(f, noBaseline)
	if prob != nil {
		t.Fatalf("an orphan must resolve from the Finding alone: %+v", prob)
	}
	if fl.Workflow != "web-retire" || fl.Params["port"] != "8443" {
		t.Fatalf("wrong launch: %+v", fl)
	}
	if !fl.Withdrawal {
		t.Fatal("it must be marked a withdrawal: retiring abandoned state is not the same act as converging live state, and the preview says which")
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
	for _, want := range []string{"f2", "pruned", "onRemove was retain", "retained deliberately"} {
		if !strings.Contains(prob.Message, want) {
			t.Errorf("the message must explain the declaration, not the missing row; want %q in: %s", want, prob.Message)
		}
	}
}

// A NON-orphan Finding whose Baseline read fails is a real error and must reach fail(), which
// owns the error→status mapping. Swallowing it into a 404 with a withdrawal-flavoured message
// would misreport an ordinary lookup failure as a deliberate retention.
func TestNonOrphanBaselineFailureIsStillAnError(t *testing.T) {
	f := types.Finding{ID: "f3", Baseline: "cis-1", Framework: "cis"}
	_, prob := resolveFindingLaunch(f, noBaseline)
	if prob == nil {
		t.Fatal("a missing baseline on a drift Finding is not resolvable")
	}
	if prob.Err == nil {
		t.Fatalf("it must be handed to fail() rather than turned into a message here: %+v", prob)
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
	if fl.Workflow != "web-converge" || fl.Withdrawal {
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
