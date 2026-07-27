package pluginserve

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The EE-Job transport carries the SAME ApplyRequest the gRPC transport does — proto-JSON on disk
// instead of protobuf on a wire (ADR-0051). This pins that equivalence: a request the core encodes
// for either transport round-trips here.
func TestReadRequestDecodesTheStagedApplyRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	raw, err := protojson.Marshal(&pluginv1.ApplyRequest{
		DryRun:  true,
		Desired: &pluginv1.Payload{Bytes: []byte(`{"playbook":"site.yml"}`)},
		Targets: []*pluginv1.ApplyTarget{{Name: "web-01", Vars: map[string]string{"a": "b"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STRATT_REQUEST", path)

	got, err := ReadRequest()
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetDryRun() {
		t.Error("the check-mode bit must survive the transport")
	}
	if string(got.GetDesired().GetBytes()) != `{"playbook":"site.yml"}` {
		t.Errorf("desired is OPAQUE and must survive byte-for-byte: %s", got.GetDesired().GetBytes())
	}
	if len(got.GetTargets()) != 1 || got.GetTargets()[0].GetName() != "web-01" {
		t.Errorf("targets: %v", got.GetTargets())
	}
}

// The failure a shim actually hits is a mis-staged mount, so the path has to be IN the error —
// "no such file" alone makes the reader redo the diagnosis (§1.8).
func TestReadRequestNamesThePathItFailedOn(t *testing.T) {
	t.Setenv("STRATT_REQUEST", "/nonexistent/stratt/request.json")
	_, err := ReadRequest()
	if err == nil {
		t.Fatal("a missing request must be an error")
	}
	if !strings.Contains(err.Error(), "/nonexistent/stratt/request.json") {
		t.Fatalf("the diagnostic must name the path: %v", err)
	}
}

func TestRunnerDirTakesTheShimsDefault(t *testing.T) {
	if got := RunnerDir("/runner"); got != "/runner" {
		t.Fatalf("unset must take the shim's own default, got %q", got)
	}
	t.Setenv("STRATT_RUNNER_DIR", "/elsewhere")
	if got := RunnerDir("/runner"); got != "/elsewhere" {
		t.Fatalf("the env override must win, got %q", got)
	}
}

// Frames are NEWLINE-DELIMITED proto-JSON: the core reads the Job's stdout line by line, so a
// frame that spanned lines or omitted the separator would corrupt every frame after it.
func TestEmitterWritesNewlineDelimitedFrames(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf)
	e.Event(pluginv1.TaskEvent_LEVEL_INFO, "step one", map[string]string{"host": "web-01"})
	e.Event(pluginv1.TaskEvent_LEVEL_INFO, "step two", nil)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("one line per frame, got %d: %q", len(lines), buf.String())
	}
	for _, ln := range lines {
		var probe map[string]any
		if err := json.Unmarshal([]byte(ln), &probe); err != nil {
			t.Fatalf("each line must be standalone JSON: %v (%q)", err, ln)
		}
	}
}

// Fatal is the terminal frame a shim emits when it cannot even start its tool. It returns nil so a
// shim can `return e.Fatal(msg)`: the FRAME is the verdict, and returning an error too would exit
// non-zero and double-report one failure.
func TestEmitterFatalIsTerminalAndReturnsNil(t *testing.T) {
	var buf bytes.Buffer
	if err := NewEmitter(&buf).Fatal("ansible-runner not found"); err != nil {
		t.Fatalf("Fatal must not also return an error: %v", err)
	}
	var got pluginv1.ApplyResponse
	if err := protojson.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ev := got.GetEvent()
	if !ev.GetTerminal() || ev.GetOk() {
		t.Error("a fatal frame is terminal and not ok — the core keys the Run's outcome on it")
	}
	if ev.GetLevel() != pluginv1.TaskEvent_LEVEL_ERROR || ev.GetMessage() != "ansible-runner not found" {
		t.Errorf("level/message: %v %q", ev.GetLevel(), ev.GetMessage())
	}
}

// A nil emitter must not panic: a shim that failed before wiring stdout still has to be able to
// return, and a panic in the reporting path destroys the only channel back to the core.
func TestNilEmitterIsSafe(t *testing.T) {
	var e *Emitter
	e.Send(&pluginv1.ApplyResponse{})
	e.Event(pluginv1.TaskEvent_LEVEL_INFO, "x", nil)
	_ = e.Fatal("y")
}
