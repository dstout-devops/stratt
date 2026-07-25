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
	// prior are the already-compiled Baselines a previous pass wrote — the input the
	// expectation-change gate diffs against (ADR-0119 D5).
	prior []types.Baseline
	// membership is what a previous pass recorded. Without it every compile looks like a FIRST
	// compile for membership, so every entity appears to "join" — which would hide the property
	// the expectation gate exists for: that a promotion moves NO members while rewriting every
	// expected value.
	membership map[string]graph.AssignmentMembership
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

// Keyed by name@version like the real store (ADR-0119 D1); the fixtures declare version 0, which
// normalizes to 1 exactly as the store does.
func (f *fakeStore) GetIntent(_ context.Context, name string, version int) (types.Intent, error) {
	if version < 1 {
		version = 1
	}
	if in, ok := f.intents[name]; ok {
		got := in.Version
		if got < 1 {
			got = 1
		}
		if got != version {
			return types.Intent{}, fmt.Errorf("no intent %s@%d", name, version)
		}
		return in, nil
	}
	return types.Intent{}, fmt.Errorf("no intent %s@%d", name, version)
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
func (f *fakeStore) GetAssignmentMembership(_ context.Context, a string) (graph.AssignmentMembership, bool, error) {
	m, ok := f.membership[a]
	return m, ok, nil
}
func (f *fakeStore) GetFacetOwner(context.Context, string) (types.FacetOwner, bool, error) {
	return types.FacetOwner{}, false, nil
}
func (f *fakeStore) ListBaselines(context.Context) ([]types.Baseline, error) { return f.prior, nil }

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

// ── the expectation-change gate (ADR-0119 D5) ─────────────────────────────────────────
//
// These exist because the ADR had to withdraw a claim: it said a promotion "goes through the
// existing compile-diff and max-delta gate like any other change", and that was verified FALSE.
// The membership gate keys on View membership, and a version bump changes expected VALUES with
// joins and leaves both empty — so `exceedsDelta` could never fire on the one change promotion
// actually makes. A bump silently rewrote every expectation for the Assignment.

// compiledPriorFor runs one compile and returns its Upserts, so a second compile has something
// to diff against — the shape a real second reconcile pass sees.
func compiledPriorFor(t *testing.T, s *fakeStore) []types.Baseline {
	t.Helper()
	plan := compileOne(t, s)
	// Record the membership that pass computed, exactly as Apply would — otherwise the next
	// compile sees no previous set and reports every entity as a join.
	s.membership = map[string]graph.AssignmentMembership{}
	for _, m := range plan.Memberships {
		s.membership[m.Assignment] = m
	}
	return plan.Upserts
}

// TestExpectationChangeIsRendered is the §1.8 half: "what would promoting this change" must be
// answerable from the plan, not inferred from a Git diff of the Intent.
func TestExpectationChangeIsRendered(t *testing.T) {
	s := appStore(map[string]any{"package": "nginx", "port": "443"}, nil, nil)
	prior := compiledPriorFor(t, s)

	// The promotion: same Assignment, a new expected value.
	s.prior = prior
	s.intents["web"] = types.Intent{
		Name: "web", Kind: types.IntentApplication,
		Spec: map[string]any{"package": "nginx", "port": "8443"},
	}
	// Acked, so the gate does not pause — this test is about the RENDERING.
	s.assignments[0].AckDelta = 1

	plan := compileOne(t, s)
	if len(plan.Deltas) != 1 {
		t.Fatalf("expected one delta, got %d", len(plan.Deltas))
	}
	ch := plan.Deltas[0].ExpectationChanges
	if len(ch) != 1 {
		t.Fatalf("expected exactly one expectation change, got %+v", ch)
	}
	if ch[0].Namespace != "app.config" || ch[0].Path != "port" {
		t.Errorf("the change must name the expectation it belongs to, got %+v", ch[0])
	}
	if !strings.Contains(ch[0].From, "443") || !strings.Contains(ch[0].To, "8443") {
		t.Errorf("the change must render both values, got from=%q to=%q", ch[0].From, ch[0].To)
	}
	// Membership did NOT change — which is precisely why this surface had to exist.
	if len(plan.Deltas[0].Joins) != 0 || len(plan.Deltas[0].Leaves) != 0 {
		t.Errorf("this change moves no members; the membership gate cannot see it: %+v", plan.Deltas[0])
	}
}

// TestExpectationChangeGatePauses is the gate half, and the one that matters: an unacknowledged
// total rewrite must NOT compile. Reporting the change while applying it would be the same
// inert-mechanism defect this arc has hit repeatedly.
func TestExpectationChangeGatePauses(t *testing.T) {
	s := appStore(map[string]any{"package": "nginx", "port": "443"}, nil, nil)
	s.prior = compiledPriorFor(t, s)
	s.intents["web"] = types.Intent{
		Name: "web", Kind: types.IntentApplication,
		Spec: map[string]any{"package": "nginx", "port": "8443"},
	}

	plan := compileOne(t, s)
	if len(plan.Upserts) != 0 {
		t.Fatalf("an unacknowledged expectation rewrite must compile NOTHING, got %d Baselines — "+
			"the live expectations have to stay in force until it is acked", len(plan.Upserts))
	}
	d := plan.Deltas[0]
	if !d.Paused {
		t.Fatal("the delta must be marked paused")
	}
	if !strings.Contains(d.Note, "expectation-change gate") || !strings.Contains(d.Note, "ackDelta") {
		t.Errorf("the note must name the gate and how to clear it (§1.8); got: %s", d.Note)
	}
	// And the changes are still rendered while paused — an operator deciding whether to ack needs
	// to see what they would be acking.
	if len(d.ExpectationChanges) == 0 {
		t.Error("a paused delta must still render what changed, or the ack is a blind signature")
	}
}

// TestAckDeltaClearsTheExpectationGate: bumping the same counter §4.3 already uses for membership
// unblocks it. One ack, both axes — deliberately, because two independent acks would let an
// operator acknowledge a membership shift while ignoring a total expectation rewrite.
func TestAckDeltaClearsTheExpectationGate(t *testing.T) {
	s := appStore(map[string]any{"package": "nginx", "port": "443"}, nil, nil)
	s.prior = compiledPriorFor(t, s)
	s.intents["web"] = types.Intent{
		Name: "web", Kind: types.IntentApplication,
		Spec: map[string]any{"package": "nginx", "port": "8443"},
	}
	s.assignments[0].AckDelta = 1

	plan := compileOne(t, s)
	if len(plan.Upserts) != 1 {
		t.Fatalf("an acknowledged change must compile, got %d Baselines", len(plan.Upserts))
	}
	if got := string(plan.Upserts[0].Expected[0].Equals); got != `"8443"` {
		t.Fatalf("the new expectation must land, got %s", got)
	}
}

// TestFirstCompileIsNotAnExpectationChange: with no prior Baseline every expectation is NEW, and
// counting those as changes would gate the very first compile of every Assignment behind an ack —
// making the estate un-bootstrappable.
func TestFirstCompileIsNotAnExpectationChange(t *testing.T) {
	s := appStore(map[string]any{"package": "nginx", "port": "443"}, nil, nil)
	plan := compileOne(t, s)
	if len(plan.Upserts) != 1 {
		t.Fatalf("a first compile must not be gated, got %d Baselines", len(plan.Upserts))
	}
	if len(plan.Deltas[0].ExpectationChanges) != 0 {
		t.Fatalf("new expectations are creates, not changes: %+v", plan.Deltas[0].ExpectationChanges)
	}
}

// TestUnchangedRecompileIsSilent: the reconcile runs every pass, so a no-op recompile must produce
// no changes and no gate. Otherwise the gate would fire continuously on a converged estate.
func TestUnchangedRecompileIsSilent(t *testing.T) {
	s := appStore(map[string]any{"package": "nginx", "port": "443"}, nil, nil)
	s.prior = compiledPriorFor(t, s)
	plan := compileOne(t, s)
	if len(plan.Deltas[0].ExpectationChanges) != 0 {
		t.Fatalf("an unchanged recompile must report nothing: %+v", plan.Deltas[0].ExpectationChanges)
	}
	if plan.Deltas[0].Paused {
		t.Fatal("an unchanged recompile must not pause")
	}
	if len(plan.Upserts) != 1 {
		t.Fatalf("and it must still compile normally, got %d", len(plan.Upserts))
	}
}

// ── withdrawal params (ADR-0118 D3, the follow-up half) ──

// removeStore is appStore plus a withdrawal Workflow and a Blueprint that declares
// removeParams — the shape the access and fileset Blueprints now ship.
func removeStore(removeParams map[string]any, inputs json.RawMessage) *fakeStore {
	s := appStore(map[string]any{"package": "nginx", "channel": "stable"},
		map[string]any{"port": "8443"}, nil)
	s.intents["web"] = types.Intent{
		Name: "web", Kind: types.IntentApplication, OnRemove: types.OnRemoveRevert,
		Spec: map[string]any{"package": "nginx", "channel": "stable"},
	}
	bp := s.blueprints["web-server@1"]
	bp.RemoveWorkflow = "web-retire"
	bp.RemoveParams = removeParams
	s.blueprints["web-server@1"] = bp
	s.workflows = map[string]types.Workflow{
		"web-retire": {Name: "web-retire", Inputs: inputs},
	}
	return s
}

var retireInputs = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port"],
  "properties": {"port": {"type": "string"}}
}`)

// The withdrawal params must be resolved from the EFFECTIVE spec — including a value declared
// only on the Assignment — and land on every Baseline the Assignment compiles. Reading
// bp.RemoveParams raw would leave `{{.spec.port}}` unsubstituted; reading only the Intent's spec
// would resolve it to nothing, since `port` here is the Assignment's declaration.
func TestRemoveParamsAreResolvedFromTheEffectiveSpec(t *testing.T) {
	s := removeStore(map[string]any{"port": "{{.spec.port}}"}, retireInputs)
	plan := compileOne(t, s)
	if len(plan.Upserts) != 1 {
		t.Fatalf("expected one compiled baseline, got %d (%v)", len(plan.Upserts), plan.Errors)
	}
	if got := plan.Upserts[0].RemoveParams["port"]; got != "8443" {
		t.Fatalf("removeParams must carry the Assignment-declared value, got %#v", got)
	}
}

// THE POINT OF THE WHOLE CHANGE. Withdraw the Assignment — which is what makes an orphan — and
// the params must still be there. They cannot be recomputed at this moment: the Assignment that
// declared `port: 8443` is gone from Git, so the compiled Baseline is the only surviving record.
// Before this, the orphan Finding named a Workflow and carried no values, and the operator had
// to reconstruct them from a deleted declaration.
func TestWithdrawalParamsSurviveTheAssignmentThatDeclaredThem(t *testing.T) {
	s := removeStore(map[string]any{"port": "{{.spec.port}}"}, retireInputs)
	compiled := compileOne(t, s).Upserts
	if len(compiled) != 1 {
		t.Fatalf("setup: expected one baseline, got %d", len(compiled))
	}

	// Now the Assignment is withdrawn: gone from Git, its Baseline still live.
	s.assignments = nil
	s.prior = compiled

	plan := compileOne(t, s)
	if len(plan.Orphans) != 1 {
		t.Fatalf("a withdrawn Assignment with onRemove=revert owes one orphan, got %d", len(plan.Orphans))
	}
	var detail map[string]any
	if err := json.Unmarshal(plan.Orphans[0].Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["removeWorkflow"] != "web-retire" {
		t.Fatalf("the orphan must still name the withdrawal Workflow: %v", detail)
	}
	params, ok := detail["removeParams"].(map[string]any)
	if !ok {
		t.Fatalf("the orphan must carry the compiled withdrawal params, got %v", detail)
	}
	if params["port"] != "8443" {
		t.Fatalf("the params must be the values the retired configuration actually ran with, got %#v", params)
	}
}

// A withdrawal Workflow wired to params it does not accept must fail the COMPILE, in front of
// the author — not at the moment an operator launches a retirement from an orphan Finding.
func TestRemoveParamsMustSatisfyTheWithdrawalWorkflowInputs(t *testing.T) {
	s := removeStore(map[string]any{"prot": "{{.spec.port}}"}, retireInputs) // typo: prot
	plan := compileOne(t, s)
	if len(plan.Upserts) != 0 {
		t.Fatalf("a route whose removeParams do not fit its Workflow must not compile, got %d", len(plan.Upserts))
	}
	if len(plan.Errors) == 0 || !strings.Contains(plan.Errors[0], "removeParams do not satisfy workflow web-retire") {
		t.Fatalf("the error must name the Workflow and the field: %v", plan.Errors)
	}
}

// Params with no Workflow to pass them to is a half-declaration: refused rather than ignored,
// the same rule remediationParams gets. Silently dropping them would mean an author who
// mistyped `removeWorkflow` gets a withdrawal path that carries nothing and says nothing.
func TestRemoveParamsWithoutARemoveWorkflowAreRefused(t *testing.T) {
	s := removeStore(map[string]any{"port": "{{.spec.port}}"}, retireInputs)
	bp := s.blueprints["web-server@1"]
	bp.RemoveWorkflow = ""
	s.blueprints["web-server@1"] = bp

	plan := compileOne(t, s)
	if len(plan.Errors) == 0 || !strings.Contains(plan.Errors[0], "removeParams declared with no removeWorkflow") {
		t.Fatalf("a half-declaration must be refused by name: %v", plan.Errors)
	}
}

// A Blueprint with a removeWorkflow but no removeParams must keep compiling exactly as before —
// the field is additive, and the estate has Blueprints that legitimately need no values.
func TestARemoveWorkflowWithoutParamsStillCompiles(t *testing.T) {
	s := removeStore(nil, retireInputs)
	plan := compileOne(t, s)
	if len(plan.Errors) != 0 {
		t.Fatalf("removeParams is optional: %v", plan.Errors)
	}
	if len(plan.Upserts) != 1 {
		t.Fatalf("expected one baseline, got %d", len(plan.Upserts))
	}
	if plan.Upserts[0].RemoveParams != nil {
		t.Fatalf("absent params must stay absent, got %#v", plan.Upserts[0].RemoveParams)
	}
}

// The withdrawal spec must reach the orphan Finding TYPED, not only inside the human-facing
// detail blob. The blob lands in graph.finding.diff, documented as redacted and size-capped, so a
// launch door that parsed its way back out of it would break silently the first time anything
// capped it. This is what makes retiring abandoned state launchable rather than merely readable.
func TestOrphanCarriesTheWithdrawalSpecTyped(t *testing.T) {
	s := removeStore(map[string]any{"port": "{{.spec.port}}"}, retireInputs)
	compiled := compileOne(t, s).Upserts
	s.assignments = nil
	s.prior = compiled

	plan := compileOne(t, s)
	if len(plan.Orphans) != 1 {
		t.Fatalf("expected one orphan, got %d", len(plan.Orphans))
	}
	o := plan.Orphans[0]
	if o.LaunchWorkflow != "web-retire" {
		t.Fatalf("the orphan must carry the withdrawal Workflow as a typed field, got %q", o.LaunchWorkflow)
	}
	if o.LaunchParams["port"] != "8443" {
		t.Fatalf("and its compiled params, got %#v", o.LaunchParams)
	}
}

// onRemove:retain (the default) must carry NO withdrawal spec: the state is deliberately left in
// place, so offering a Workflow to retire it would misrepresent the declaration. The orphan
// Finding still exists — abandoned state is never silent (§2.4) — it just has nothing to launch.
func TestRetainedOrphanCarriesNoWithdrawalSpec(t *testing.T) {
	s := removeStore(map[string]any{"port": "{{.spec.port}}"}, retireInputs)
	in := s.intents["web"]
	in.OnRemove = "" // retain, the default
	s.intents["web"] = in
	compiled := compileOne(t, s).Upserts
	s.assignments = nil
	s.prior = compiled

	plan := compileOne(t, s)
	if len(plan.Orphans) != 1 {
		t.Fatalf("a retained withdrawal still owes an orphan Finding, got %d", len(plan.Orphans))
	}
	if plan.Orphans[0].LaunchWorkflow != "" || plan.Orphans[0].LaunchParams != nil {
		t.Fatalf("onRemove=retain must offer nothing to launch, got %+v", plan.Orphans[0])
	}
}
