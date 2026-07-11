package ansible

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func TestParseEventAndFacts(t *testing.T) {
	// Shape emitted by `ansible-runner run -j`: one JSON event per line.
	line := []byte(`{"uuid":"u1","counter":7,"event":"runner_on_ok","stdout":"ok: [vm-1]","event_data":{"host":"vm-1","res":{"ansible_facts":{"ansible_system":"Linux","ansible_kernel":"6.6.87","ansible_architecture":"x86_64"}}}}`)
	ev, ok := ParseEvent(line)
	if !ok {
		t.Fatal("event line did not parse")
	}
	if ev.Event != "runner_on_ok" || ev.Counter != 7 {
		t.Fatalf("unexpected event: %+v", ev)
	}

	re := ToRunEvent("run-1", ev)
	if re.Target != "vm-1" || re.Seq != 7 || re.Kind != "runner_on_ok" {
		t.Fatalf("unexpected run event: %+v", re)
	}

	host, status := HostStatus(ev)
	if host != "vm-1" || status != "ok" {
		t.Fatalf("unexpected host status: %s %s", host, status)
	}

	facts := ExtractFacts(ev)
	if facts == nil {
		t.Fatal("expected facts")
	}
	if string(facts["os.kernel"]) != `{"arch":"x86_64","family":"linux","release":"6.6.87"}` {
		t.Fatalf("unexpected os.kernel facet: %s", facts["os.kernel"])
	}

	// Non-event noise must be ignored, not fail.
	if _, ok := ParseEvent([]byte("some banner text")); ok {
		t.Fatal("noise line should not parse as an event")
	}
}

func TestActuatorSeam(t *testing.T) {
	a := Actuator{}
	if a.Name() != "ansible" {
		t.Fatalf("name: %s", a.Name())
	}
	spec, err := a.Prepare(nil, []Target{{EntityID: "e1", Name: "vm-1", Vars: map[string]string{"ansible_connection": "local"}}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Files["project/play.yml"] == "" || spec.Files["inventory/hosts"] == "" {
		t.Fatalf("content files missing: %+v", spec.Files)
	}
	if spec.Command[0] != "ansible-runner" {
		t.Fatalf("command: %v", spec.Command)
	}

	iv, ok := a.Interpret([]byte(`{"uuid":"u1","counter":9,"event":"runner_on_failed","stdout":"","event_data":{"host":"vm-1"}}`))
	if !ok {
		t.Fatal("event should interpret")
	}
	if iv.Event.Seq != 9 || iv.Event.Kind != "runner_on_failed" || iv.Event.Target != "vm-1" {
		t.Fatalf("event mapping: %+v", iv.Event)
	}
	if iv.Result == nil || !iv.Result.Failed || iv.Result.Status != actuators.StatusFailed {
		t.Fatalf("failed host must fold into a failed result: %+v", iv.Result)
	}

	// Distinct statuses: changed (ok + mutation) and unreachable (a failure).
	iv, _ = a.Interpret([]byte(`{"counter":10,"event":"runner_on_ok","event_data":{"host":"vm-2","res":{"changed":true}}}`))
	if iv.Result == nil || iv.Result.Status != actuators.StatusChanged || iv.Result.Failed {
		t.Fatalf("changed result: %+v", iv.Result)
	}
	iv, _ = a.Interpret([]byte(`{"counter":11,"event":"runner_on_unreachable","event_data":{"host":"vm-3"}}`))
	if iv.Result == nil || iv.Result.Status != actuators.StatusUnreachable || !iv.Result.Failed {
		t.Fatalf("unreachable result: %+v", iv.Result)
	}
}

func TestPrepareParams(t *testing.T) {
	a := Actuator{}
	targets := []Target{{EntityID: "e1", Name: "vm-1"}}

	// Custom play + EE override plumb through.
	spec, err := a.Prepare(json.RawMessage(`{"play":"- hosts: all\n  tasks: []\n","eeImage":"stratt-ee:custom"}`), targets)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Files["project/play.yml"], "hosts: all") || spec.Image != "stratt-ee:custom" {
		t.Fatalf("params not applied: image=%q files=%v", spec.Image, spec.Files["project/play.yml"])
	}

	// Invalid YAML and non-sequence plays fail at Prepare.
	if _, err := a.Prepare(json.RawMessage(`{"play":"{ not: [valid"}`), targets); err == nil {
		t.Fatal("garbage play must be rejected")
	}
	if _, err := a.Prepare(json.RawMessage(`{"play":"hosts: all"}`), targets); err == nil {
		t.Fatal("non-sequence play must be rejected")
	}

	// Empty params keep the Phase-0 default.
	spec, err = a.Prepare(nil, targets)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Files["project/play.yml"] != GatherFactsPlay || spec.Image != "" {
		t.Fatal("empty params must yield the gather-facts default")
	}
}

func TestBuildContent(t *testing.T) {
	c := BuildContent(GatherFactsPlay, []Target{
		{EntityID: "e1", Name: "vm-1", Vars: map[string]string{"ansible_connection": "local"}},
		{EntityID: "e2", Name: "vm-2", Vars: map[string]string{"ansible_connection": "local"}},
	})
	want := "[all]\nvm-1 ansible_connection=local\nvm-2 ansible_connection=local\n"
	if c.Hosts != want {
		t.Fatalf("hosts:\n got %q\nwant %q", c.Hosts, want)
	}
	if c.Play == "" {
		t.Fatal("empty play")
	}
}
