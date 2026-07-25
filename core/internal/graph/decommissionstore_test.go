package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// A decommission Finding must be LAUNCHABLE through the ordinary remediation door.
//
// It was not. ADR-0114 D4 built the whole teardown reach-path — resolve the build provider's teardown
// Workflow, surface a gated Finding per excess unit — and then recorded the Workflow and the target's
// identity only inside the `diff` detail blob. `resolveFindingLaunch` reads `LaunchWorkflow` first and
// otherwise falls through to a Baseline read; there IS no `decommission/<intent>` Baseline (nothing
// creates one), so every teardown answered 404 "no baseline and no launch spec, nothing to launch".
// The operator's remaining option was to read the blob and hand-launch the Workflow by name with a
// retyped identity — which also sidesteps D4's build-provenance anchor, the entire point of resolving
// the teardown of the provider that built the thing.
//
// So this test is about the door, not the columns: what it pins is that the Finding carries everything
// a launch needs.
func TestDecommissionFindingCarriesALaunchableSpec(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	f := DecommissionFinding{
		Baseline: "decommission/web-fleet", Target: "web-05", Severity: "warning",
		Detail:         json.RawMessage(`{"reason":"built but no longer desired (count-down)"}`),
		LaunchWorkflow: "vsphere-vm-teardown",
		LaunchParams: map[string]any{
			"instance": "web-05", "intent": "web-fleet", "ordinal": 5,
			"identityScheme": "vcenter.uuid", "identityValue": "421f-abcd",
		},
	}
	if err := s.WriteDecommissionFinding(ctx, f); err != nil {
		t.Fatal(err)
	}

	got := oneFinding(t, s, "decommission/web-fleet")
	if got.LaunchWorkflow != "vsphere-vm-teardown" {
		t.Fatalf("the teardown Workflow must ride the Finding, not only its detail blob; got %q", got.LaunchWorkflow)
	}
	// `remove`, deliberately NOT a fourth enum member: a count-down teardown retires state whose
	// declaration no longer asks for it, which is exactly what `remove` already names (ADR-0120 D1
	// left "a fourth act must argue membership"; this one does not need to).
	if got.LaunchKind != types.LaunchRemove {
		t.Fatalf("a teardown is a `remove`; got %q", got.LaunchKind)
	}
	if got.LaunchParams["identityValue"] != "421f-abcd" {
		t.Fatalf("the teardown target must ride the launch spec: %#v", got.LaunchParams)
	}
}

// The spec is DERIVED from Git plus a live projection, so it must be REDERIVED on an already-open
// Finding — the same invariant the provision Finding needed, and one line of SQL away from wrong. A
// rebound provider or a re-observed identity that did not reach the open row would hand the operator
// a destructive launch against yesterday's target.
func TestDecommissionLaunchSpecIsRederivedEveryPass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first := DecommissionFinding{
		Baseline: "decommission/web-fleet", Target: "web-05", Severity: "warning",
		Detail:         json.RawMessage(`{"reason":"first pass"}`),
		LaunchWorkflow: "vsphere-vm-teardown",
		LaunchParams:   map[string]any{"instance": "web-05", "identityValue": "stale-uuid"},
	}
	if err := s.WriteDecommissionFinding(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.LaunchWorkflow = "awsec2-teardown"
	second.LaunchParams = map[string]any{"instance": "web-05", "identityValue": "i-fresh"}
	if err := s.WriteDecommissionFinding(ctx, second); err != nil {
		t.Fatal(err)
	}

	got := oneFinding(t, s, "decommission/web-fleet")
	if got.LaunchWorkflow != "awsec2-teardown" {
		t.Errorf("a rebound teardown provider must reach the open Finding, got %q — the DO UPDATE is not refreshing launch_workflow", got.LaunchWorkflow)
	}
	if got.LaunchParams["identityValue"] != "i-fresh" {
		t.Errorf("a re-observed identity must reach the open Finding, got %#v — an operator would destroy the wrong target", got.LaunchParams)
	}
}

// Unresolved teardown ⇒ NO launch spec, and the Finding still surfaces. Both halves matter: the
// operator must know the unit is undesired (§1.8), and must not be offered a launch that cannot work
// or a destructive one against a target core had to guess (§2.4).
func TestAnUnresolvedTeardownCarriesNothingToLaunch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.WriteDecommissionFinding(ctx, DecommissionFinding{
		Baseline: "decommission/web-fleet", Target: "web-05", Severity: "warning",
		Detail: json.RawMessage(`{"unresolved":"no provider advertises a Compute teardown"}`),
	}); err != nil {
		t.Fatal(err)
	}
	got := oneFinding(t, s, "decommission/web-fleet")
	if got.LaunchWorkflow != "" || got.LaunchKind != "" || len(got.LaunchParams) != 0 {
		t.Fatalf("an unresolved teardown must offer nothing to launch: %q %q %#v",
			got.LaunchWorkflow, got.LaunchKind, got.LaunchParams)
	}
}
