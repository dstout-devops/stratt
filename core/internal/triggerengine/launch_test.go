package triggerengine

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// This package had NO test files. That is how the Workflow-target launch path came to send an
// event and nothing else for as long as it did, and why falsifying "drop the inputs" produced a
// green suite: there was nothing to fail.
//
// workflowDAGInput is the pure core of that path, so what a firing Trigger actually sends is
// checkable without a Temporal client.

func TestWorkflowDAGInputCarriesEventAndInputs(t *testing.T) {
	tr := types.Trigger{
		Name: "on-host-added", Kind: types.TriggerEvent, Emitter: "em", When: "true",
		WorkflowName: "onboard", Principal: "svc",
		// The launch interface, bound from the firing payload (ADR-0118 D5).
		Inputs: map[string]any{"host": "{{.event.hostname}}", "tier": "web"},
	}
	payload := map[string]any{"hostname": "web-07", "extra": "ignored"}

	in, err := workflowDAGInput(tr, payload, "dev")
	if err != nil {
		t.Fatalf("workflowDAGInput: %v", err)
	}
	if in.WorkflowName != "onboard" || in.Principal != "svc" || in.Trigger != "on-host-added" {
		t.Fatalf("target/lineage lost: %+v", in)
	}
	// BOTH must travel. The event still rides so each Step resolves its own {{.event.x}}
	// bindings (ADR-0024 D2 — unchanged); the inputs ride so the Workflow's own declared
	// interface can be filled. Sending only one of the two was the defect.
	if in.Event["hostname"] != "web-07" {
		t.Errorf("the firing payload must still ride into the DAG, got %#v", in.Event)
	}
	if in.LaunchParams["host"] != "web-07" {
		t.Errorf("a templated input must be substituted from the payload, got %#v", in.LaunchParams)
	}
	if in.LaunchParams["tier"] != "web" {
		t.Errorf("a literal input must pass through, got %#v", in.LaunchParams)
	}
}

// TestWorkflowDAGInputUnresolvableBindingErrors: a binding referencing a field the payload does
// not carry is a TERMINAL data error, surfaced to the caller so the event can be dropped rather
// than redelivered forever (ADR-0024 D6 — a poison message must not loop).
func TestWorkflowDAGInputUnresolvableBindingErrors(t *testing.T) {
	_, err := workflowDAGInput(types.Trigger{
		Name: "t", WorkflowName: "w", Inputs: map[string]any{"host": "{{.event.missing}}"},
	}, map[string]any{"hostname": "web-07"}, "dev")
	if err == nil {
		t.Fatal("a binding the payload cannot satisfy must error, not silently resolve to empty")
	}
}

// TestWorkflowDAGInputWithoutInputs: the field is optional — a Workflow needing nothing must be
// launchable by an event Trigger exactly as before.
func TestWorkflowDAGInputWithoutInputs(t *testing.T) {
	in, err := workflowDAGInput(types.Trigger{Name: "t", WorkflowName: "w"}, map[string]any{"a": 1}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if in.LaunchParams != nil {
		t.Fatalf("no inputs declared ⇒ none carried, got %#v", in.LaunchParams)
	}
	if in.Event["a"] != 1 {
		t.Fatalf("the payload must still ride, got %#v", in.Event)
	}
}
