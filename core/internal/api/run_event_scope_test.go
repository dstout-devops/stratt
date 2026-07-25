package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The scope must reach a client when stated and be ABSENT when not (ADR-0121 D1). Same rule the level
// already follows, and for the same reason: `""` on the wire would make "the producer did not say"
// indistinguishable from a stated value, and unstated must never read as `task` — the field is newer
// than most producers, so defaulting it would assert that nothing has ever described a Run (§1.8).
func TestRunEventToWireCarriesScopeAndOmitsAbsence(t *testing.T) {
	stated := runEventToWire(types.RunEvent{
		RunID: "r", Seq: 3, Kind: "ee-content", Level: types.RunEventInfo,
		Scope:   types.RunEventScopeRun,
		Payload: map[string]any{"message": "community.crypto 2.22.3"},
	})
	if stated.Scope == nil || *stated.Scope != RunEventScope(types.RunEventScopeRun) {
		t.Fatalf("a stated run scope must reach the wire, got %v", stated.Scope)
	}
	body, err := json.Marshal(stated)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"scope":"run"`) {
		t.Fatalf("scope missing from the serialized event: %s", body)
	}

	silent := runEventToWire(types.RunEvent{RunID: "r", Seq: 1, Kind: "stdout"})
	if silent.Scope != nil {
		t.Fatalf("an unstated scope must stay absent, got %q", *silent.Scope)
	}
	body, err = json.Marshal(silent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "scope") {
		t.Fatalf("an unstated scope must not appear on the wire at all: %s", body)
	}
}

// Scope and Target must be able to coexist without contradicting each other, which is the shape D2
// chose: `target` is the ONLY field that names an Entity, so a per-target event is `task` + a target.
// Pinned because the rejected design — a SCOPE_TARGET member — would have made these two fields two
// discriminators for one question, the §2.4 defect ADR-0120 D1 found between Framework and launchKind.
func TestScopeAndTargetAnswerDifferentQuestions(t *testing.T) {
	perTarget := runEventToWire(types.RunEvent{
		RunID: "r", Seq: 9, Kind: "runner_on_ok",
		Scope: types.RunEventScopeTask, Target: "web-01",
	})
	if perTarget.Scope == nil || *perTarget.Scope != RunEventScope(types.RunEventScopeTask) {
		t.Fatalf("a per-target event is task-scoped, got %v", perTarget.Scope)
	}
	if perTarget.Target == nil || *perTarget.Target != "web-01" {
		t.Fatalf("the target must still name the Entity: %v", perTarget.Target)
	}
	// And the enum must NOT have grown a per-target member behind our back.
	if RunEventScope("target").Valid() {
		t.Error("a per-target scope member would duplicate `target` and could disagree with it (ADR-0121 D2)")
	}
}
