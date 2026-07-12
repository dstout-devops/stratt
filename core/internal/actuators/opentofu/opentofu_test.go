package opentofu

import (
	"encoding/json"
	"strings"
	"testing"
)

func testActuator() Actuator {
	return Actuator{
		BackendURL:   "http://strattd:8080",
		Credential:   func(ws string) string { return "cred-" + ws },
		DefaultImage: "stratt-ee-tofu:dev",
	}
}

func TestPrepare(t *testing.T) {
	spec, err := testActuator().Prepare(json.RawMessage(`{
		"module": "resource \"terraform_data\" \"x\" {}",
		"mode": "plan", "workspace": "demo",
		"vars": {"count": 3}
	}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Files["project/backend.tf"], `"http://strattd:8080/statebackend/demo"`) {
		t.Fatalf("backend.tf must point at the workspace state URL:\n%s", spec.Files["project/backend.tf"])
	}
	if strings.Contains(spec.Files["project/backend.tf"], "cred-demo") {
		t.Fatal("credential must never land in files (§2.5) — env only")
	}
	if spec.Env["TF_HTTP_PASSWORD"] != "cred-demo" {
		t.Fatalf("env credential: %v", spec.Env)
	}
	if spec.Image != "stratt-ee-tofu:dev" {
		t.Fatalf("image: %q", spec.Image)
	}
	if !strings.Contains(spec.Files["project/stratt.auto.tfvars.json"], `"count":3`) {
		t.Fatalf("vars file: %q", spec.Files["project/stratt.auto.tfvars.json"])
	}

	// Backend unconfigured → refuse (never plaintext local state).
	if _, err := (Actuator{}).Prepare(json.RawMessage(`{"module":"x","mode":"plan","workspace":"w"}`), nil); err == nil {
		t.Fatal("unconfigured backend must refuse Prepare")
	}
	// Defense-in-depth on mode.
	if _, err := testActuator().Prepare(json.RawMessage(`{"module":"x","mode":"destroy","workspace":"w"}`), nil); err == nil {
		t.Fatal("unknown mode must be refused")
	}
}

func TestInterpret(t *testing.T) {
	a := testActuator()

	// planned_change
	ev, ok := a.Interpret([]byte(`{"counter":3,"tofu":{"type":"planned_change","@message":"terraform_data.x: Plan to create","@level":"info","change":{"resource":{"addr":"terraform_data.x"},"action":"create"}}}`))
	if !ok || ev.Event.Kind != "planned_change" || ev.Event.Seq != 3 {
		t.Fatalf("planned_change: %+v ok=%v", ev, ok)
	}

	// change_summary carries the counts.
	ev, ok = a.Interpret([]byte(`{"counter":4,"tofu":{"type":"change_summary","@message":"Plan: 2 to add, 0 to change, 1 to destroy.","changes":{"add":2,"change":0,"remove":1,"operation":"plan"}}}`))
	if !ok || ev.Event.Kind != "change_summary" || ev.Event.Payload["add"] != 2 || ev.Event.Payload["remove"] != 1 {
		t.Fatalf("change_summary: %+v", ev.Event.Payload)
	}

	// diagnostics surface severity + summary (§1.8).
	ev, ok = a.Interpret([]byte(`{"counter":5,"tofu":{"type":"diagnostic","@level":"error","@message":"Error: Unsupported block type","diagnostic":{"severity":"error","summary":"Unsupported block type","detail":"Blocks of type \"nope\" are not expected here."}}}`))
	if !ok || ev.Event.Kind != "diagnostic" || ev.Event.Payload["severity"] != "error" {
		t.Fatalf("diagnostic: %+v", ev.Event.Payload)
	}

	// plan_json rides one event.
	ev, ok = a.Interpret([]byte(`{"counter":6,"event":"plan_json","plan":{"format_version":"1.2"}}`))
	if !ok || ev.Event.Kind != "plan-json" {
		t.Fatalf("plan-json: %+v", ev.Event)
	}

	// terminal: plan ok → ok; apply ok → changed; rc!=0 → failed.
	ev, _ = a.Interpret([]byte(`{"counter":9,"event":"tofu_finished","rc":0,"mode":"plan"}`))
	if ev.Result == nil || ev.Result.Status != "ok" || ev.Result.Failed {
		t.Fatalf("plan finish: %+v", ev.Result)
	}
	ev, _ = a.Interpret([]byte(`{"counter":9,"event":"tofu_finished","rc":0,"mode":"apply"}`))
	if ev.Result == nil || ev.Result.Status != "changed" {
		t.Fatalf("apply finish: %+v", ev.Result)
	}
	ev, _ = a.Interpret([]byte(`{"counter":9,"event":"tofu_finished","rc":1,"mode":"apply"}`))
	if ev.Result == nil || !ev.Result.Failed {
		t.Fatalf("failed finish: %+v", ev.Result)
	}

	// Non-driver noise is not an event.
	if _, ok := a.Interpret([]byte(`Terraform has been successfully initialized!`)); ok {
		t.Fatal("banner noise must not interpret")
	}
}

func TestInterpretOutputs(t *testing.T) {
	a := testActuator()
	line := []byte(`{"counter":12,"event":"outputs_json","outputs":{
		"stratt_entities":{"sensitive":false,"type":["list",["object",{"kind":"string","identityKeys":["map","string"],"labels":["map","string"]}]],
			"value":[{"kind":"endpoint","identityKeys":{"tofu.id":"ep-1"},"labels":{"demo":"tofu"}}]},
		"admin_password":{"sensitive":true,"type":"string","value":"(sensitive)"}
	}}`)
	ev, ok := a.Interpret(line)
	if !ok || ev.Event.Kind != "outputs-json" {
		t.Fatalf("outputs-json: %+v ok=%v", ev.Event, ok)
	}
	if len(ev.Entities) != 1 || ev.Entities[0].Kind != "endpoint" || ev.Entities[0].IdentityKeys["tofu.id"] != "ep-1" {
		t.Fatalf("entities: %+v", ev.Entities)
	}
	if len(ev.OutputsContract) == 0 {
		t.Fatal("outputs contract must derive")
	}

	// Malformed reserved output → failed result with the contract named.
	bad := []byte(`{"counter":13,"event":"outputs_json","outputs":{
		"stratt_entities":{"sensitive":false,"type":"string","value":[{"kind":"","identityKeys":{}}]}
	}}`)
	ev, ok = a.Interpret(bad)
	if !ok || ev.Result == nil || !ev.Result.Failed || ev.Event.Kind != "invalid-entities" {
		t.Fatalf("malformed stratt_entities must fail the run: %+v", ev)
	}
}
