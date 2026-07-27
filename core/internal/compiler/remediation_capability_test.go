package compiler

import (
	"context"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/capability"
	"github.com/dstout-devops/stratt/types"
)

// Capability-routed remediation (ADR-0135 D3), tested against the fake Store for the reason
// wiring_test.go's preamble gives: a DB-gated test cannot prove a mechanism is CALLED, and an
// inert seam is this project's recurring defect. Every test here drives Compile().

// capRouteStore is appStore with the route's remediation expressed as a CAPABILITY rather than a
// Workflow name, plus the Workflow a provider would resolve to.
func capRouteStore(capClass string) *fakeStore {
	s := appStore(map[string]any{"port": "8080"}, nil, nil)
	s.blueprints["web-server@1"].Routes[0].RemediationCapability = capClass
	s.workflows = map[string]types.Workflow{
		"web-server-configure": {Name: "web-server-configure"},
		"puppet-converge":      {Name: "puppet-converge"},
	}
	return s
}

// resolverTo is a RemediationResolver that always resolves to wf, recording what it was asked.
func resolverTo(wf string, seen *[]string) RemediationResolver {
	return func(_ context.Context, capClass, intentKind string) (capability.Result, error) {
		*seen = append(*seen, capClass+"/"+intentKind)
		return capability.Result{Status: capability.StatusResolved, Provider: "p", Workflow: wf}, nil
	}
}

// TestRemediationCapability_ResolvesOntoTheBaseline is the mechanism's whole point: a route naming
// a class compiles a Baseline carrying a CONCRETE Workflow. Resolution happens once, at compile —
// so one-click descent still shows one answer, and nothing downstream learns about capabilities.
func TestRemediationCapability_ResolvesOntoTheBaseline(t *testing.T) {
	var seen []string
	plan := compileWith(t, capRouteStore(types.CapConfigMgmt), resolverTo("web-server-configure", &seen))

	if len(plan.Errors) > 0 {
		t.Fatalf("compile errors: %v", plan.Errors)
	}
	if len(plan.Upserts) != 1 {
		t.Fatalf("expected 1 compiled Baseline, got %d", len(plan.Upserts))
	}
	if got := plan.Upserts[0].RemediationWorkflow; got != "web-server-configure" {
		t.Errorf("Baseline.RemediationWorkflow = %q, want the RESOLVED workflow — a capability that does not reach the Baseline is an indirection to nowhere", got)
	}
	// The resolver is asked for the class the route named, keyed by the BARE Intent kind — the
	// same convention provisions/decommissions use, which is what lets capability.Resolve be
	// shared. "Intent/Application" here would silently miss every provider's map.
	if len(seen) != 1 || seen[0] != types.CapConfigMgmt+"/Application" {
		t.Errorf("resolver asked for %v, want [configmgmt/Application] (bare kind, no Intent/ prefix)", seen)
	}
}

// TestRemediationCapability_ClaimsOwnership: a route that remediates MANAGES its namespace (§2.1),
// and that claim is made from RemediationWorkflow. Resolving before the claim is computed is what
// keeps a capability-routed Blueprint owning its Facet exactly as a name-routed one does — get the
// order wrong and a whole class of Blueprint silently stops claiming ownership.
func TestRemediationCapability_ClaimsOwnership(t *testing.T) {
	var seen []string
	plan := compileWith(t, capRouteStore(types.CapConfigMgmt), resolverTo("web-server-configure", &seen))
	if len(plan.Errors) > 0 {
		t.Fatalf("compile errors: %v", plan.Errors)
	}
	if len(plan.Ownership) == 0 {
		t.Fatal("a capability-routed remediation must still claim namespace ownership — it manages the Facet just as a named Workflow does (§2.1)")
	}
}

// TestRemediationCapability_FailsClosed covers the three ways resolution does not produce a
// Workflow. All three must fail the ASSIGNMENT with the resolver's own reason, never compile a
// Baseline whose remediation is silently absent (§1.8, §2.4 — no silent tiebreak).
func TestRemediationCapability_FailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		resolve  RemediationResolver
		wantText string
	}{
		{
			name: "pending — no verified provider",
			resolve: func(context.Context, string, string) (capability.Result, error) {
				return capability.Result{Status: capability.StatusPending, Reason: "no verified provider builds this"}, nil
			},
			wantText: "no verified provider",
		},
		{
			name: "ambiguous — two providers, no binding",
			resolve: func(context.Context, string, string) (capability.Result, error) {
				return capability.Result{Status: capability.StatusAmbiguous, Reason: "2 verified providers — add a capability-binding"}, nil
			},
			wantText: "add a capability-binding",
		},
		{
			name:     "no resolver wired at all",
			resolve:  nil,
			wantText: "no capability resolver",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := compileWith(t, capRouteStore(types.CapConfigMgmt), tc.resolve)
			if len(plan.Upserts) != 0 {
				t.Fatalf("a route that cannot resolve must compile NO Baseline, got %d", len(plan.Upserts))
			}
			joined := strings.Join(plan.Errors, "; ")
			if !strings.Contains(joined, tc.wantText) {
				t.Fatalf("error %q does not carry the resolver's reason %q — the operator needs the fix, not the symptom", joined, tc.wantText)
			}
		})
	}
}

// TestRemediationCapability_ResolvedWorkflowMustExist: resolution names a Workflow, and a provider
// advertising one the estate does not declare is still an authoring error. The existing
// existence check must run AFTER resolution, or a capability route skips it entirely.
func TestRemediationCapability_ResolvedWorkflowMustExist(t *testing.T) {
	var seen []string
	plan := compileWith(t, capRouteStore(types.CapConfigMgmt), resolverTo("workflow-that-does-not-exist", &seen))
	if len(plan.Upserts) != 0 {
		t.Fatalf("expected no Baseline, got %d", len(plan.Upserts))
	}
	if !strings.Contains(strings.Join(plan.Errors, "; "), "not found") {
		t.Fatalf("a resolved-but-undeclared Workflow must fail the same way a named one does: %v", plan.Errors)
	}
}

// TestRemediationCapability_RebindChangesTheCompiledWorkflow is the property the whole decision
// exists for, and the one a reader should be able to point at: the SAME Blueprint compiles to a
// DIFFERENT remediation when the estate binds a different provider. Nothing in the Blueprint,
// Intent, or Assignment changes between these two compiles.
//
// It also pins the ADR's trap: a rebind must RECOMPILE. If resolution ever moved to Run time this
// still passes, so the Baseline assertion above is what guards that half.
func TestRemediationCapability_RebindChangesTheCompiledWorkflow(t *testing.T) {
	var seen []string
	before := compileWith(t, capRouteStore(types.CapConfigMgmt), resolverTo("web-server-configure", &seen))
	after := compileWith(t, capRouteStore(types.CapConfigMgmt), resolverTo("puppet-converge", &seen))

	if len(before.Upserts) != 1 || len(after.Upserts) != 1 {
		t.Fatalf("both compiles must produce a Baseline: %d, %d", len(before.Upserts), len(after.Upserts))
	}
	if before.Upserts[0].RemediationWorkflow == after.Upserts[0].RemediationWorkflow {
		t.Fatal("rebinding the capability must change the compiled remediation — otherwise the indirection buys nothing")
	}
	if after.Upserts[0].RemediationWorkflow != "puppet-converge" {
		t.Errorf("after rebind: %q", after.Upserts[0].RemediationWorkflow)
	}
	// And the Baseline is otherwise identical — a rebind changes WHO converges, not what is
	// expected. If this drifts, a provider swap silently rewrites desired state.
	if before.Upserts[0].Name != after.Upserts[0].Name {
		t.Errorf("a rebind must not rename the Baseline: %q vs %q", before.Upserts[0].Name, after.Upserts[0].Name)
	}
}

// TestNameRoutedRemediationIsUntouched: `remediationWorkflow` is kept, not deprecated (D3), and a
// nil resolver must not disturb it. This is the "changes nothing for every already-shipped
// Blueprint" guarantee.
func TestNameRoutedRemediationIsUntouched(t *testing.T) {
	s := appStore(map[string]any{"port": "8080"}, nil, nil)
	s.blueprints["web-server@1"].Routes[0].RemediationWorkflow = "web-server-configure"
	s.workflows = map[string]types.Workflow{"web-server-configure": {Name: "web-server-configure"}}

	plan := compileWith(t, s, nil) // no resolver at all
	if len(plan.Errors) > 0 {
		t.Fatalf("a name-routed Blueprint must compile with no resolver: %v", plan.Errors)
	}
	if plan.Upserts[0].RemediationWorkflow != "web-server-configure" {
		t.Errorf("name-routed remediation changed: %q", plan.Upserts[0].RemediationWorkflow)
	}
}
