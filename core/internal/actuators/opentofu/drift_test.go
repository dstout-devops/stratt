package opentofu

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func interpretLine(t *testing.T, line string) actuators.Interpreted {
	t.Helper()
	out, ok := Actuator{}.Interpret([]byte(line))
	if !ok {
		t.Fatalf("line must interpret: %s", line)
	}
	return out
}

func TestInterpretPlanDrift(t *testing.T) {
	// A plan's change_summary with pending changes escalates the workspace
	// to changed — "tofu plan on cron is drift detection" (ADR-0019).
	out := interpretLine(t, `{"counter":5,"tofu":{"type":"change_summary","@message":"Plan: 1 to add","changes":{"add":1,"change":0,"remove":0,"operation":"plan"}}}`)
	if out.Result == nil || out.Result.Status != actuators.StatusChanged || out.Result.Failed {
		t.Fatalf("plan with changes must report changed: %+v", out.Result)
	}
	var frag map[string]any
	if err := json.Unmarshal(out.Drift, &frag); err != nil || frag["add"] != float64(1) {
		t.Fatalf("drift fragment: %s (%v)", out.Drift, err)
	}

	// A clean plan's change_summary: no result, no drift.
	out = interpretLine(t, `{"counter":5,"tofu":{"type":"change_summary","@message":"Plan: 0","changes":{"add":0,"change":0,"remove":0,"operation":"plan"}}}`)
	if out.Result != nil || out.Drift != nil {
		t.Fatalf("clean plan must observe nothing: %+v %s", out.Result, out.Drift)
	}

	// An apply's change_summary is not a drift observation.
	out = interpretLine(t, `{"counter":9,"tofu":{"type":"change_summary","@message":"Apply complete","changes":{"add":1,"change":0,"remove":0,"operation":"apply"}}}`)
	if out.Result != nil || out.Drift != nil {
		t.Fatalf("apply summary must observe nothing: %+v %s", out.Result, out.Drift)
	}

	// The terminal ok of a clean plan stays ok; the dispatcher's escalation
	// fold keeps an earlier changed sticky over it.
	out = interpretLine(t, `{"counter":10,"event":"tofu_finished","rc":0,"mode":"plan"}`)
	if out.Result == nil || out.Result.Status != actuators.StatusOK {
		t.Fatalf("plan terminal: %+v", out.Result)
	}
}

func TestInterpretResourceFragments(t *testing.T) {
	out := interpretLine(t, `{"counter":3,"tofu":{"type":"planned_change","@message":"null_resource.a: Plan to create","change":{"resource":{"addr":"null_resource.a"},"action":"create"}}}`)
	if out.Result != nil {
		t.Fatalf("planned_change is not a terminal result: %+v", out.Result)
	}
	var frag map[string]any
	if err := json.Unmarshal(out.Drift, &frag); err != nil ||
		frag["address"] != "null_resource.a" || frag["action"] != "create" {
		t.Fatalf("planned_change fragment: %s (%v)", out.Drift, err)
	}

	out = interpretLine(t, `{"counter":4,"tofu":{"type":"resource_drift","@message":"drifted","change":{"resource":{"addr":"null_resource.b"},"action":"update"}}}`)
	if err := json.Unmarshal(out.Drift, &frag); err != nil || frag["drift"] != true || frag["address"] != "null_resource.b" {
		t.Fatalf("resource_drift fragment: %s (%v)", out.Drift, err)
	}
}
