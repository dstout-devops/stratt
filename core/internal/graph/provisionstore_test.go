package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// A provision Finding's launch spec is DERIVED from Git and must be REDERIVED every reconcile —
// the opposite of an orphan Finding's, which is the only surviving record of a configuration that
// has left Git and is therefore written once (ADR-0120 D2).
//
// This exists because the invariant is one line of SQL away from being wrong. WriteProvisionFinding
// upserts on the live (baseline, target) row, and its DO UPDATE originally refreshed only `diff`.
// Left that way, an already-open Finding would keep serving the params from its FIRST reconcile, so
// an edit to labels or placement — or a CapabilityBinding change that swaps the build Workflow —
// would launch yesterday's desired state from a Finding that looks current. That is the second
// truth §1.2 forbids, and nothing else in the suite would have noticed.
func TestProvisionFindingLaunchSpecIsRederivedEveryPass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := ProvisionFinding{
		Baseline: "provision/web-fleet", Target: "web-02", Severity: "warning",
		Detail:         json.RawMessage(`{"reason":"declared but not built"}`),
		LaunchWorkflow: "compute-build",
		LaunchParams: map[string]any{
			"instance": "web-02",
			"labels":   map[string]any{"fleet": "web", "stratt.intent/instance": "web-02"},
		},
	}
	if err := s.WriteProvisionFinding(ctx, first); err != nil {
		t.Fatal(err)
	}

	got := oneFinding(t, s, "provision/web-fleet")
	if got.LaunchWorkflow != "compute-build" {
		t.Fatalf("launchWorkflow: got %q", got.LaunchWorkflow)
	}
	if got.LaunchKind != types.LaunchBuild {
		t.Fatalf("a provisioning build must be stamped %q, got %q", types.LaunchBuild, got.LaunchKind)
	}
	if got.LaunchParams["instance"] != "web-02" {
		t.Fatalf("launchParams: %#v", got.LaunchParams)
	}

	// Git changes: the fleet gains a label and the binding swaps the builder. The SAME open Finding
	// must now describe the new desired state.
	second := first
	second.LaunchWorkflow = "vsphere-vm-build"
	second.LaunchParams = map[string]any{
		"instance": "web-02",
		"labels":   map[string]any{"fleet": "web", "tier": "edge", "stratt.intent/instance": "web-02"},
	}
	if err := s.WriteProvisionFinding(ctx, second); err != nil {
		t.Fatal(err)
	}

	got = oneFinding(t, s, "provision/web-fleet")
	if got.LaunchWorkflow != "vsphere-vm-build" {
		t.Fatalf("a rebound builder must reach the open Finding, got %q — the DO UPDATE is not refreshing launch_workflow", got.LaunchWorkflow)
	}
	labels, _ := got.LaunchParams["labels"].(map[string]any)
	if labels["tier"] != "edge" {
		t.Fatalf("an edited label must reach the open Finding, got %#v — the DO UPDATE is not refreshing launch_params", got.LaunchParams)
	}
}

// Unresolved provisioning carries NO launch spec: there is genuinely nothing to launch, and the
// detail names the capability that could not be bound. The all-or-nothing DB constraint means a
// kind cannot be stamped without a Workflow either.
func TestUnresolvedProvisionFindingCarriesNoLaunchSpec(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.WriteProvisionFinding(ctx, ProvisionFinding{
		Baseline: "provision/orphan-kind", Target: "thing-01", Severity: "warning",
		Detail: json.RawMessage(`{"unresolved":"no verified provisioning provider"}`),
	}); err != nil {
		t.Fatal(err)
	}
	got := oneFinding(t, s, "provision/orphan-kind")
	if got.LaunchWorkflow != "" || got.LaunchKind != "" || got.LaunchParams != nil {
		t.Fatalf("unresolved provisioning must offer nothing to launch, got %+v", got)
	}
}

func oneFinding(t *testing.T, s *Store, baseline string) types.Finding {
	t.Helper()
	fs, err := s.ListFindings(context.Background(), baseline, "open", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected exactly one open finding for %s, got %d", baseline, len(fs))
	}
	return fs[0]
}
