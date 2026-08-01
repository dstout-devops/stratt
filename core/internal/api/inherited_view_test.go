package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// A converge Workflow is a RECIPE, not a target. The Assignment says WHERE (`view:`), the compiler
// copies it onto the Baseline, and a Step re-stating it was one value with two binding sites able
// to disagree (§2.4) — and unusable for any host outside the View its author happened to name.
//
// These cover the AUTHZ half, which is where making viewName optional could have gone wrong: the
// old door skipped every Step with no View, so an omitted field would have become a bypassed gate.
func actuationStep(name, view string) types.Step {
	return types.Step{Name: name, ViewName: view, Actuator: "ansible"}
}

func TestAuthorizeLaunch_InheritedViewIsStillChecked(t *testing.T) {
	s := &Server{}
	wf := types.Workflow{Name: "apache-configure", Steps: []types.Step{actuationStep("converge", "")}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)

	// No principal in context and no grant → the inherited View must still be CHECKED, so this
	// must not succeed. The old code skipped the Step entirely and returned ok.
	if _, ok := s.authorizeLaunch(w, r, wf, "managed-app"); ok {
		t.Fatal("an inherited View must be authorized, not skipped — an omitted field is not a bypassed gate (§2.5)")
	}
}

// An actuation Step with no View AND nothing to inherit is refused, naming the Step. It would
// otherwise run against nothing.
func TestAuthorizeLaunch_UnboundActuationStepIsRefused(t *testing.T) {
	s := &Server{}
	wf := types.Workflow{Name: "apache-configure", Steps: []types.Step{actuationStep("converge", "")}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	if _, ok := s.authorizeLaunch(w, r, wf, ""); ok {
		t.Fatal("an actuation Step with no View and none to inherit must be refused")
	}
	if !strings.Contains(w.Body.String(), "converge") || !strings.Contains(w.Body.String(), "apache-configure") {
		t.Fatalf("the refusal must name the workflow and the step: %s", w.Body.String())
	}
}

// Steps that target no View are unaffected — a Gate has nothing to authorize.
func TestAuthorizeLaunch_NonActuationStepsNeedNoView(t *testing.T) {
	s := &Server{}
	wf := types.Workflow{Name: "w", Steps: []types.Step{{Name: "approve", Gate: &types.GateSpec{}}}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", nil)
	if _, ok := s.authorizeLaunch(w, r, wf, ""); !ok {
		t.Fatalf("a gate-only workflow must authorize with no View: %s", w.Body.String())
	}
}

// The predicate the authz door and the DAG dispatch share.
func TestIsActuation(t *testing.T) {
	if !(types.Step{Actuator: "ansible"}).IsActuation() {
		t.Fatal("an actuator step is actuation")
	}
	for _, st := range []types.Step{
		{Gate: &types.GateSpec{}},
		{Policy: &types.PolicySpec{}},
		{Workflow: "child"},
		{Action: "netbox/ipam-resolve"},
		{ActionCapability: "ipam"},
	} {
		if st.IsActuation() {
			t.Fatalf("%+v must not be actuation — it targets no View", st)
		}
	}
}
