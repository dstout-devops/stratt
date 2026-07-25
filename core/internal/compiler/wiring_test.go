package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// This file exists because of a falsification result, not a plan.
//
// After wiring ADR-0118 D1 (the Assignment values layer + compile-time completeness
// validation + specLayers), both mechanisms were tested by DELETING them: removing the
// `{Name: "assignment:"…}` layer from Compile, and short-circuiting the
// validateResolvedSpec call. The whole suite stayed GREEN both times.
//
// The reason: `integration_test.go` is the only test that drives Compile(), and it
// t.Skip()s without a live Postgres — so in `task ci` nothing exercised the compiler's
// wiring at all. Unit tests covered validateResolvedSpec in isolation but never proved it
// was CALLED, and nothing proved the third layer was plumbed.
//
// That is precisely the inert-mechanism defect this project keeps finding (ADR-0117: a
// declared `eeImage` read by nothing; D5c's discarded terminal). A DB-gated test cannot
// guard against it, so these use a fake Store and run everywhere.

// fakeStore is the minimum Store the compiler needs, with no substrate. Everything is a
// literal so a test reads as the estate it describes.
type fakeStore struct {
	intents     map[string]types.Intent
	assignments []types.Assignment
	views       map[string]types.View
	blueprints  map[string]types.Blueprint // "name@version"
	workflows   map[string]types.Workflow
	entities    []types.Entity
}

func (f *fakeStore) ListIntents(context.Context) ([]types.Intent, error) {
	out := make([]types.Intent, 0, len(f.intents))
	for _, in := range f.intents {
		out = append(out, in)
	}
	return out, nil
}
func (f *fakeStore) ListAssignments(context.Context) ([]types.Assignment, error) {
	return f.assignments, nil
}
func (f *fakeStore) GetIntent(_ context.Context, name string) (types.Intent, error) {
	if in, ok := f.intents[name]; ok {
		return in, nil
	}
	return types.Intent{}, fmt.Errorf("no intent %q", name)
}
func (f *fakeStore) GetView(_ context.Context, name string) (types.View, error) {
	if v, ok := f.views[name]; ok {
		return v, nil
	}
	return types.View{}, fmt.Errorf("no view %q", name)
}
func (f *fakeStore) ResolveSelector(context.Context, types.ViewSelector, map[string]any, int) ([]types.Entity, error) {
	return f.entities, nil
}
func (f *fakeStore) GetBlueprint(_ context.Context, name string, version int) (types.Blueprint, error) {
	if bp, ok := f.blueprints[fmt.Sprintf("%s@%d", name, version)]; ok {
		return bp, nil
	}
	return types.Blueprint{}, fmt.Errorf("no blueprint %s@%d", name, version)
}
func (f *fakeStore) GetWorkflow(_ context.Context, name string) (types.Workflow, error) {
	if w, ok := f.workflows[name]; ok {
		return w, nil
	}
	return types.Workflow{}, fmt.Errorf("no workflow %q", name)
}
func (f *fakeStore) GetAssignmentMembership(context.Context, string) (graph.AssignmentMembership, bool, error) {
	return graph.AssignmentMembership{}, false, nil // first compile: no previous set
}
func (f *fakeStore) GetFacetOwner(context.Context, string) (types.FacetOwner, bool, error) {
	return types.FacetOwner{}, false, nil
}
func (f *fakeStore) ListBaselines(context.Context) ([]types.Baseline, error) { return nil, nil }

// appStore builds a minimal compilable estate: one Intent/Application, one Blueprint whose
// route observes app.config.port from {{.spec.port}}, one cac View with one member.
func appStore(intentSpec, assignmentValues map[string]any, defaults map[string]any) *fakeStore {
	return &fakeStore{
		intents: map[string]types.Intent{
			"web": {Name: "web", Kind: types.IntentApplication, Spec: intentSpec},
		},
		assignments: []types.Assignment{{
			Name: "prod-web", Intent: "web", View: "hosts",
			Blueprint: "web-server", BlueprintVersion: 1,
			Environments: []string{"prod"}, Values: assignmentValues,
		}},
		views: map[string]types.View{
			"hosts": {Name: "hosts", DeclaredBy: graph.DeclaredByCaC, Selector: types.ViewSelector{Kinds: []string{"host"}}},
		},
		blueprints: map[string]types.Blueprint{
			"web-server@1": {
				Name: "web-server", Version: 1, For: types.IntentApplication, Defaults: defaults,
				Routes: []types.BlueprintRoute{{
					Observe: types.FacetExpectation{
						Namespace: "app.config", Path: "port",
						Equals: json.RawMessage(`"{{.spec.port}}"`),
					},
					Claim: types.ClaimExclusive,
				}},
			},
		},
		entities: []types.Entity{{ID: "e1", Kind: "host"}},
	}
}

func compileOne(t *testing.T, s Store) Plan {
	t.Helper()
	plan, err := Compile(context.Background(), s, DefaultMaxDelta)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}

// TestAssignmentValuesReachTheResolvedSpec is the wiring proof: a value declared ONLY on
// the Assignment must appear in the compiled Baseline's expectation, which is what
// "config-as-code reaches the thing that executes" means at this layer. Deleting the
// assignment layer from Compile fails this test.
func TestAssignmentValuesReachTheResolvedSpec(t *testing.T) {
	s := appStore(
		map[string]any{"package": "nginx"}, // Intent declares the package…
		map[string]any{"port": "8443"},     // …the Assignment declares the port
		nil,
	)
	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("unexpected compile errors: %v", plan.Errors)
	}
	if len(plan.Upserts) != 1 {
		t.Fatalf("expected 1 compiled Baseline, got %d", len(plan.Upserts))
	}
	got := string(plan.Upserts[0].Expected[0].Equals)
	if got != `"8443"` {
		t.Fatalf("an Assignment-declared value must reach the expectation, got %s", got)
	}
}

// TestSpecLayersLandOnTheCompiledBaseline proves the §1.8 descent artifact is persisted
// rather than computed and dropped (which is what the compiler did before ADR-0118 D1).
func TestSpecLayersLandOnTheCompiledBaseline(t *testing.T) {
	s := appStore(
		map[string]any{"package": "nginx"},
		map[string]any{"port": "8443"},
		map[string]any{"channel": "stable"},
	)
	plan := compileOne(t, s)
	origin := plan.Upserts[0].CompiledFrom
	if origin == nil || len(origin.SpecLayers) == 0 {
		t.Fatal("compiledFrom.specLayers must be persisted so the value's source is answerable")
	}
	for path, wantLayer := range map[string]string{
		"package": "intent:web",
		"port":    "assignment:prod-web",
		"channel": "blueprint:web-server/defaults",
	} {
		layers, ok := origin.SpecLayers[path]
		if !ok || len(layers) == 0 {
			t.Errorf("specLayers is missing %q", path)
			continue
		}
		if last := layers[len(layers)-1]; last != wantLayer {
			t.Errorf("path %q should trace to %q, got %v", path, wantLayer, layers)
		}
	}
}

// TestIntentAndAssignmentDoubleClaimSkipsTheAssignment is the anti-GPO rule end to end:
// two co-equal declarations of one value must fail the compile, name both layers, and
// compile NOTHING for that Assignment — never silently pick a winner.
func TestIntentAndAssignmentDoubleClaimSkipsTheAssignment(t *testing.T) {
	s := appStore(
		map[string]any{"package": "nginx", "port": "443"}, // both declare port
		map[string]any{"port": "8443"},
		nil,
	)
	plan := compileOne(t, s)
	if len(plan.Upserts) != 0 {
		t.Fatalf("a double-claimed Assignment must compile no Baselines, got %d", len(plan.Upserts))
	}
	if len(plan.Errors) == 0 {
		t.Fatal("a double claim must surface a compile error (§1.8), not resolve silently")
	}
	joined := strings.Join(plan.Errors, "\n")
	for _, want := range []string{"intent:web", "assignment:prod-web", "port"} {
		if !strings.Contains(joined, want) {
			t.Errorf("compile error must name %q; got: %s", want, joined)
		}
	}
}

// TestBlueprintDefaultIsOverriddenNotClaimed: the same shape as the double claim, except
// the earlier layer is a DEFAULT — which yields by definition, so this must compile
// cleanly. Without the Yielding distinction the app-cert demo (whose Blueprint default and
// Intent both set port) would fail to compile.
func TestBlueprintDefaultIsOverriddenNotClaimed(t *testing.T) {
	s := appStore(
		map[string]any{"package": "nginx", "port": "443"},
		nil,
		map[string]any{"port": "80"}, // default, yields
	)
	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("a default under a declaration must not be a claim conflict: %v", plan.Errors)
	}
	if got := string(plan.Upserts[0].Expected[0].Equals); got != `"443"` {
		t.Fatalf("the declaration must win over the default, got %s", got)
	}
}

// TestResolvedSpecCompletenessIsEnforcedAtCompile proves validateResolvedSpec is actually
// CALLED. Short-circuiting that call used to leave the suite green — the exact defect this
// file was added for.
//
// Intent/Certificate requires issuer + commonName + renewBefore; supplying two of three
// across the layers must fail the Assignment rather than compile a Baseline from a spec
// that does not satisfy its kind.
func TestResolvedSpecCompletenessIsEnforcedAtCompile(t *testing.T) {
	s := appStore(nil, nil, nil)
	s.intents["web"] = types.Intent{
		Name: "web", Kind: types.IntentCertificate,
		Spec: map[string]any{"issuer": "cert-issuer/dev"}, // missing commonName + renewBefore
	}
	bp := s.blueprints["web-server@1"]
	bp.For = types.IntentCertificate
	s.blueprints["web-server@1"] = bp

	plan := compileOne(t, s)
	if len(plan.Upserts) != 0 {
		t.Fatalf("an incomplete resolved spec must compile nothing, got %d Baselines", len(plan.Upserts))
	}
	if len(plan.Errors) == 0 {
		t.Fatal("an incomplete resolved spec must surface a compile error")
	}
	if joined := strings.Join(plan.Errors, "\n"); !strings.Contains(joined, "resolved spec") {
		t.Errorf("the error should identify itself as a resolved-spec failure; got: %s", joined)
	}
}

// TestLayersCompleteASpecAcrossDeclarations is the payoff of the validation move: neither
// the Intent nor the Assignment is a complete Certificate spec, but together they are — so
// this compiles, where declaration-time completeness would have rejected both documents.
func TestLayersCompleteASpecAcrossDeclarations(t *testing.T) {
	s := appStore(nil, nil, nil)
	s.intents["web"] = types.Intent{
		Name: "web", Kind: types.IntentCertificate,
		Spec: map[string]any{"issuer": "cert-issuer/dev", "commonName": "web.test"},
	}
	s.assignments[0].Values = map[string]any{"renewBefore": "360h"} // the missing third
	bp := s.blueprints["web-server@1"]
	bp.For = types.IntentCertificate
	// Observe a field a Certificate spec actually has; the shared fixture's route reads
	// {{.spec.port}}, which this kind does not define.
	bp.Routes[0].Observe = types.FacetExpectation{
		Namespace: "cert.identity", Path: "commonName",
		Equals: json.RawMessage(`"{{.spec.commonName}}"`),
	}
	s.blueprints["web-server@1"] = bp

	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("a spec completed ACROSS layers must compile: %v", plan.Errors)
	}
	if len(plan.Upserts) != 1 {
		t.Fatalf("expected 1 compiled Baseline, got %d", len(plan.Upserts))
	}
}

// ── route → Baseline remediation params (ADR-0118 D3) ─────────────────────────────────
//
// These close the defect the whole ADR started from: a Blueprint route named the Workflow
// that converges the estate and passed it NOTHING, so every remediation Workflow re-declared
// by hand what its Intent already said — which is why `port: "443"` appeared three times in
// one demo.

// withRemediation wires the fixture's route to a Workflow with the given input schema, and
// gives the route params substituted from the spec.
func withRemediation(s *fakeStore, inputs string, params map[string]any) *fakeStore {
	s.workflows = map[string]types.Workflow{
		"fix-it": {Name: "fix-it", Inputs: json.RawMessage(inputs), Steps: []types.Step{{Name: "go", ViewName: "v", Actuator: "script"}}},
	}
	bp := s.blueprints["web-server@1"]
	bp.Routes[0].RemediationWorkflow = "fix-it"
	bp.Routes[0].RemediationParams = params
	s.blueprints["web-server@1"] = bp
	return s
}

const fixItInputs = `{"type":"object","additionalProperties":false,` +
	`"required":["port"],"properties":{"port":{"type":"string"},"channel":{"type":"string"}}}`

// TestRemediationParamsResolveFromTheSpecOntoTheBaseline is the wire itself: a value declared
// ONCE in the Intent layer reaches the Workflow that fixes drift, with no second declaration.
func TestRemediationParamsResolveFromTheSpecOntoTheBaseline(t *testing.T) {
	s := withRemediation(
		appStore(map[string]any{"package": "nginx", "port": "8443"}, nil, nil),
		fixItInputs,
		map[string]any{"port": "{{.spec.port}}"},
	)
	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("unexpected compile errors: %v", plan.Errors)
	}
	got := plan.Upserts[0].RemediationParams
	if got["port"] != "8443" {
		t.Fatalf("the route's params must resolve from the spec, got %#v", got)
	}
}

// TestRemediationParamsResolveFromAnAssignmentValue: the params see the WHOLE resolved spec,
// so a per-environment value on the Assignment reaches the remediation too. Without this the
// environment-specific fix would silently use the fleet-wide value.
func TestRemediationParamsResolveFromAnAssignmentValue(t *testing.T) {
	s := withRemediation(
		appStore(map[string]any{"package": "nginx"}, map[string]any{"port": "9443"}, nil),
		fixItInputs,
		map[string]any{"port": "{{.spec.port}}"},
	)
	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("unexpected compile errors: %v", plan.Errors)
	}
	if got := plan.Upserts[0].RemediationParams["port"]; got != "9443" {
		t.Fatalf("an Assignment-declared value must reach the remediation, got %#v", got)
	}
}

// TestRouteParamsUnknownToTheWorkflowFailCompile: the cross-check. A route wired to a Workflow
// it does not fit breaks in front of the declaration's author, not the operator answering a
// Finding at 3am.
func TestRouteParamsUnknownToTheWorkflowFailCompile(t *testing.T) {
	s := withRemediation(
		appStore(map[string]any{"package": "nginx", "port": "8443"}, nil, nil),
		fixItInputs,
		map[string]any{"port": "{{.spec.port}}", "prt": "typo"},
	)
	plan := compileOne(t, s)
	if len(plan.Upserts) != 0 {
		t.Fatalf("a mismatched route must compile nothing, got %d Baselines", len(plan.Upserts))
	}
	joined := strings.Join(plan.Errors, "\n")
	if !strings.Contains(joined, "fix-it") || !strings.Contains(joined, "prt") {
		t.Fatalf("the error must name the workflow and the offending key; got: %s", joined)
	}
}

// TestRouteMissingARequiredInputFailsCompile: the other direction — the Workflow demands an
// input the route never passes, which would otherwise fail only at launch.
func TestRouteMissingARequiredInputFailsCompile(t *testing.T) {
	s := withRemediation(
		appStore(map[string]any{"package": "nginx", "port": "8443"}, nil, nil),
		fixItInputs,
		map[string]any{"channel": "stable"}, // `port` is required and absent
	)
	plan := compileOne(t, s)
	if len(plan.Errors) == 0 {
		t.Fatal("a route omitting a required input must fail the compile")
	}
	if len(plan.Upserts) != 0 {
		t.Fatalf("nothing may compile from a route that cannot launch, got %d", len(plan.Upserts))
	}
}

// TestRouteParamsWithNoRemediationWorkflowRejected: params with nothing to pass them to is a
// half-declaration — accepted-and-ignored is the shape ADR-0117 kept finding (a declared port
// with no address, facetNamespaces with no identityScheme).
func TestRouteParamsWithNoRemediationWorkflowRejected(t *testing.T) {
	s := appStore(map[string]any{"package": "nginx", "port": "8443"}, nil, nil)
	bp := s.blueprints["web-server@1"]
	bp.Routes[0].RemediationParams = map[string]any{"port": "{{.spec.port}}"}
	s.blueprints["web-server@1"] = bp
	plan := compileOne(t, s)
	if len(plan.Errors) == 0 {
		t.Fatal("remediationParams with no remediationWorkflow must be rejected, not ignored")
	}
}

// TestRouteWithoutParamsStillCompiles: the whole feature is additive. Every existing route
// declares no remediationParams and must be untouched.
func TestRouteWithoutParamsStillCompiles(t *testing.T) {
	s := withRemediation(appStore(map[string]any{"package": "nginx", "port": "8443"}, nil, nil), fixItInputs, nil)
	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("a route with no params must compile exactly as before: %v", plan.Errors)
	}
	if plan.Upserts[0].RemediationParams != nil {
		t.Fatalf("no params declared ⇒ none carried, got %#v", plan.Upserts[0].RemediationParams)
	}
}
