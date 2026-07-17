package script

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeRunner stands in for the script subprocess: it returns a per-target rc (by the
// target name in the env) and emits canned output lines.
type fakeRunner struct {
	rcByHost map[string]int
	lines    []string
	gotEnv   map[string][]string // host → env, so the test asserts STRATT_VAR_* mapping
}

func (f *fakeRunner) run(_ context.Context, _ /*interp*/, _ /*path*/ string, env []string, onLine func(string)) (int, error) {
	host := ""
	for _, e := range env {
		if strings.HasPrefix(e, "STRATT_TARGET_NAME=") {
			host = strings.TrimPrefix(e, "STRATT_TARGET_NAME=")
		}
	}
	if f.gotEnv == nil {
		f.gotEnv = map[string][]string{}
	}
	f.gotEnv[host] = env
	for _, l := range f.lines {
		onLine(l)
	}
	return f.rcByHost[host], nil
}

func runShim(t *testing.T, req Request, f *fakeRunner) []*pluginv1.ApplyResponse {
	t.Helper()
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, t.TempDir(), req, f); err != nil {
		t.Fatalf("run: %v", err)
	}
	var out []*pluginv1.ApplyResponse
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		r := &pluginv1.ApplyResponse{}
		if err := protojson.Unmarshal(sc.Bytes(), r); err != nil {
			t.Fatalf("emitted line is not a decodable ApplyResponse: %v\n%s", err, sc.Bytes())
		}
		out = append(out, r)
	}
	return out
}

// TestShim_PerTargetFanOut proves the shim runs the script once per LEGIBLE target and
// maps each rc onto a port ItemResult (ADR-0051): OK for rc=0, FAILED for rc≠0, the
// terminal folds not-OK when any target failed, output surfaces as diagnostics, and
// connection Vars reach the script as STRATT_VAR_<KEY>.
func TestShim_PerTargetFanOut(t *testing.T) {
	req := Request{
		Params: json.RawMessage(`{"script":"echo hi","interpreter":"sh"}`),
		Targets: []Target{
			{Name: "web-1", Vars: map[string]string{"ansible.host": "10.0.0.1"}},
			{Name: "web-2"},
		},
	}
	f := &fakeRunner{rcByHost: map[string]int{"web-1": 0, "web-2": 3}, lines: []string{"hello from script"}}
	resps := runShim(t, req, f)

	perHost := map[string]pluginv1.ItemResult_Status{}
	var diagnostics, terminals int
	var termOk bool
	for _, r := range resps {
		if res := r.GetResult(); res != nil {
			perHost[res.GetItemKey()] = res.GetStatus()
		}
		if ev := r.GetEvent(); ev != nil {
			if ev.GetFields()["kind"] == "target_output" {
				diagnostics++
			}
			if ev.GetTerminal() {
				terminals++
				termOk = ev.GetOk()
			}
		}
	}
	if perHost["web-1"] != pluginv1.ItemResult_STATUS_OK || perHost["web-2"] != pluginv1.ItemResult_STATUS_FAILED {
		t.Fatalf("per-target rc fold wrong: %+v", perHost)
	}
	if terminals != 1 || termOk {
		t.Fatalf("one terminal, ok=false (a target failed): terminals=%d ok=%v", terminals, termOk)
	}
	if diagnostics < 2 {
		t.Fatalf("each target's output must surface as a diagnostic, got %d", diagnostics)
	}
	// Vars reach the script under the legacy STRATT_VAR_<KEY> vocabulary.
	var sawVar bool
	for _, e := range f.gotEnv["web-1"] {
		if e == "STRATT_VAR_ANSIBLE_HOST=10.0.0.1" {
			sawVar = true
		}
	}
	if !sawVar {
		t.Fatalf("connection var must reach the script as STRATT_VAR_ANSIBLE_HOST: %v", f.gotEnv["web-1"])
	}
}

// TestShim_MissingScriptFatal proves an empty params.script emits a terminal fatal
// (a domain failure on the typed channel), never a silent success.
func TestShim_MissingScriptFatal(t *testing.T) {
	resps := runShim(t, Request{Params: json.RawMessage(`{}`), Targets: []Target{{Name: "h"}}}, &fakeRunner{})
	if len(resps) != 1 || !resps[0].GetEvent().GetTerminal() || resps[0].GetEvent().GetOk() {
		t.Fatalf("missing script must emit a terminal not-ok, got %+v", resps)
	}
}
