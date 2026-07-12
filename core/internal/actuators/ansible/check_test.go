package ansible

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareCheckMode(t *testing.T) {
	targets := []Target{{EntityID: "e1", Name: "vm-1"}}

	spec, err := Actuator{}.Prepare(json.RawMessage(`{"check":true}`), targets)
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(spec.Command, " ")
	if !strings.Contains(cmd, "--cmdline --check --diff") {
		t.Fatalf("check mode must pass --check --diff: %v", spec.Command)
	}

	spec, err = Actuator{}.Prepare(nil, targets)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(spec.Command, " "), "--check") {
		t.Fatalf("check must be opt-in: %v", spec.Command)
	}
}

func TestExtractDiff(t *testing.T) {
	changed := RunnerEvent{Event: "runner_on_ok", EventData: map[string]any{
		"host": "vm-1", "task": "harden sshd",
		"res": map[string]any{"changed": true, "diff": []any{map[string]any{
			"before": "PermitRootLogin yes\npassword hunter2\n", "after": "PermitRootLogin no\n",
			"after_header": "/etc/ssh/sshd_config",
		}}},
	}}
	frag := ExtractDiff(changed)
	var doc map[string]any
	if err := json.Unmarshal(frag, &doc); err != nil || doc["task"] != "harden sshd" {
		t.Fatalf("changed task must yield a fragment: %s (%v)", frag, err)
	}
	// §2.5 (guardian on ADR-0019): structure only — the changed object's
	// header, never the before/after bodies (which can carry secrets).
	if paths, _ := doc["paths"].([]any); len(paths) != 1 || paths[0] != "/etc/ssh/sshd_config" {
		t.Fatalf("fragment must carry diff headers: %s", frag)
	}
	if strings.Contains(string(frag), "hunter2") || strings.Contains(string(frag), "PermitRootLogin") {
		t.Fatalf("fragment must never carry diff bodies: %s", frag)
	}

	unchanged := RunnerEvent{Event: "runner_on_ok", EventData: map[string]any{
		"host": "vm-1", "res": map[string]any{"changed": false},
	}}
	if ExtractDiff(unchanged) != nil {
		t.Fatalf("unchanged task must yield nothing")
	}
	if ExtractDiff(RunnerEvent{Event: "runner_on_failed"}) != nil {
		t.Fatalf("failed task must yield nothing")
	}

	// The seam end-to-end: Interpret carries the fragment and the changed
	// status together.
	line := `{"counter":7,"event":"runner_on_ok","event_data":{"host":"vm-1","task":"sysctl","res":{"changed":true,"diff":[{"before":"0","after":"1"}]}}}`
	out, ok := Actuator{}.Interpret([]byte(line))
	if !ok || out.Result == nil || out.Result.Status != "changed" || out.Drift == nil {
		t.Fatalf("interpret: ok=%v result=%+v drift=%s", ok, out.Result, out.Drift)
	}
}
