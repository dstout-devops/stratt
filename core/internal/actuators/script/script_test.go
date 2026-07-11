package script

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

var targets = []actuators.Target{
	{EntityID: "e-1", Name: "vm-a", Vars: map[string]string{"env": "dev"}},
	{EntityID: "e-2", Name: "vm-b"},
}

func TestPrepare(t *testing.T) {
	spec, err := Actuator{}.Prepare(json.RawMessage(`{"script":"echo hi"}`), targets)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Files["project/script"] != "echo hi" {
		t.Fatalf("script content not rendered: %+v", spec.Files)
	}
	var step struct {
		Interpreter string `json:"interpreter"`
		Targets     []struct {
			Name     string `json:"name"`
			EntityID string `json:"entityId"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(spec.Files["project/step.json"]), &step); err != nil {
		t.Fatalf("step.json not valid JSON: %v", err)
	}
	if step.Interpreter != "sh" {
		t.Fatalf("default interpreter should be sh, got %q", step.Interpreter)
	}
	if len(step.Targets) != 2 || step.Targets[0].Name != "vm-a" || step.Targets[1].EntityID != "e-2" {
		t.Fatalf("targets not rendered: %+v", step.Targets)
	}
	if !strings.Contains(spec.Files["project/driver.py"], "target_finished") {
		t.Fatal("driver content missing")
	}
	if spec.Command[0] != "python3" {
		t.Fatalf("driver must run under python3, got %v", spec.Command)
	}
}

func TestPrepareValidation(t *testing.T) {
	if _, err := (Actuator{}).Prepare(json.RawMessage(`{}`), targets); err == nil {
		t.Fatal("missing script must be rejected")
	}
	if _, err := (Actuator{}).Prepare(json.RawMessage(`{"script":"x","interpreter":"perl"}`), targets); err == nil {
		t.Fatal("unsupported interpreter must be rejected")
	}
	if _, err := (Actuator{}).Prepare(json.RawMessage(`{"script":"x","interpreter":"python3"}`), targets); err != nil {
		t.Fatalf("python3 interpreter should be accepted: %v", err)
	}
}

func TestInterpret(t *testing.T) {
	a := Actuator{}

	if _, ok := a.Interpret([]byte("not json banner noise")); ok {
		t.Fatal("non-JSON lines must be skipped")
	}

	got, ok := a.Interpret([]byte(`{"counter":3,"event":"target_output","host":"vm-a","stream":"stdout","line":"hello"}`))
	if !ok {
		t.Fatal("output event should parse")
	}
	if got.Event.Seq != 3 || got.Event.Kind != "target_output" || got.Event.Target != "vm-a" {
		t.Fatalf("event mapping wrong: %+v", got.Event)
	}
	if got.Event.Payload["line"] != "hello" || got.Result != nil {
		t.Fatalf("output event must carry the line and no result: %+v", got)
	}

	got, ok = a.Interpret([]byte(`{"counter":4,"event":"target_finished","host":"vm-a","rc":0}`))
	if !ok || got.Result == nil || got.Result.Failed || got.Result.Target != "vm-a" {
		t.Fatalf("rc=0 must yield ok result: %+v", got)
	}
	got, _ = a.Interpret([]byte(`{"counter":9,"event":"target_finished","host":"vm-b","rc":2}`))
	if got.Result == nil || !got.Result.Failed {
		t.Fatalf("rc!=0 must yield failed result: %+v", got)
	}
	if got.Event.Payload["rc"] != 2 {
		t.Fatalf("rc must pass through the payload for diagnosis: %+v", got.Event.Payload)
	}
}

// TestDriverEndToEnd executes the real driver under the host python3 against
// a temp /runner layout — the driver is tool content, so this is a content
// test, not a control-plane Python test.
func TestDriverEndToEnd(t *testing.T) {
	runDriver(t, `{"script":"echo hello from $STRATT_TARGET_NAME"}`, 0, map[string]int{"vm-a": 0, "vm-b": 0})
	// vm-b fails; driver exit must be non-zero and per-target rc preserved.
	runDriver(t, `{"script":"test \"$STRATT_TARGET_NAME\" != vm-b"}`, 1, map[string]int{"vm-a": 0, "vm-b": 1})
}

func runDriver(t *testing.T, paramsJSON string, wantExit int, wantRC map[string]int) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	spec, err := Actuator{}.Prepare(json.RawMessage(paramsJSON), targets)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for key, content := range spec.Files {
		name := strings.TrimPrefix(key, "project/")
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", filepath.Join(dir, "driver.py"), dir)
	out, err := cmd.Output()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("driver did not run: %v", err)
	}
	if exit != wantExit {
		t.Fatalf("driver exit = %d, want %d (output: %s)", exit, wantExit, out)
	}

	// Every line must interpret; fold terminal rc per target.
	gotRC := map[string]int{}
	var lastSeq int64
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		iv, ok := Actuator{}.Interpret(sc.Bytes())
		if !ok {
			t.Fatalf("driver emitted uninterpretable line: %s", sc.Text())
		}
		if iv.Event.Seq <= lastSeq {
			t.Fatalf("counters must be strictly increasing: %d after %d", iv.Event.Seq, lastSeq)
		}
		lastSeq = iv.Event.Seq
		if iv.Event.Kind == "target_finished" {
			gotRC[iv.Event.Target] = iv.Event.Payload["rc"].(int)
		}
	}
	if len(gotRC) != len(wantRC) {
		t.Fatalf("terminal events per target: got %v want %v", gotRC, wantRC)
	}
	for host, rc := range wantRC {
		if gotRC[host] != rc {
			t.Fatalf("target %s rc = %d, want %d", host, gotRC[host], rc)
		}
	}
}
