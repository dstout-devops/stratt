package awsec2

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func TestDeclarations(t *testing.T) {
	a := CreateVM("stratt-ee-actions:dev")
	if a.Name() != "awsec2/create-vm" || a.Idempotent() || !a.DryRunnable() {
		t.Fatalf("declaration: %+v", a)
	}
}

func TestPrepareValidatesAndSetsImage(t *testing.T) {
	a := CreateVM("stratt-ee-actions:dev")
	if _, err := a.Prepare(json.RawMessage(`{"region":"us-east-1"}`), false); err == nil {
		t.Fatal("missing ami must be rejected")
	}
	spec, err := a.Prepare(json.RawMessage(`{"region":"us-east-1","ami":"ami-1"}`), false)
	if err != nil {
		t.Fatalf("valid prepare: %v", err)
	}
	if spec.Image != "stratt-ee-actions:dev" || len(spec.Command) == 0 {
		t.Fatalf("spec must set the actions EE image + command: %+v", spec)
	}
}

func TestInterpretCreatedProjectsEntity(t *testing.T) {
	line := []byte(`{"counter":1,"event":"vm_created","host":"web","ok":true,"instanceId":"i-123","privateIp":"10.0.0.5","region":"us-east-1"}`)
	iv, ok := CreateVM("x").Interpret(line)
	if !ok || len(iv.Outputs) == 0 || len(iv.Entities) != 1 {
		t.Fatalf("interpret: ok=%v outputs=%s entities=%d", ok, iv.Outputs, len(iv.Entities))
	}
	if iv.Entities[0].Kind != "instance" || iv.Entities[0].IdentityKeys["aws.instanceId"] != "i-123" {
		t.Fatalf("entity: %+v", iv.Entities[0])
	}
	var out map[string]any
	_ = json.Unmarshal(iv.Outputs, &out)
	if out["instanceId"] != "i-123" || out["privateIp"] != "10.0.0.5" {
		t.Fatalf("outputs: %v", out)
	}
	if iv.Result == nil || iv.Result.Status != actuators.StatusChanged {
		t.Fatalf("result: %+v", iv.Result)
	}
}

func TestInterpretPlannedNoEntity(t *testing.T) {
	iv, ok := CreateVM("x").Interpret([]byte(`{"counter":1,"event":"vm_planned","host":"web","ok":true,"region":"us-east-1"}`))
	if !ok || len(iv.Entities) != 0 || len(iv.Outputs) != 0 {
		t.Fatalf("a plan must not project an entity or bindable outputs: %+v", iv)
	}
}
