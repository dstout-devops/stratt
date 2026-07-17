package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	mcpcanon "github.com/dstout-devops/stratt/sdk/mcp"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeTransport returns canned JSON-RPC results by method — the shim's protocol
// mapping is exercised without a real MCP server.
type fakeTransport struct {
	results map[string]json.RawMessage
	calls   []string
}

func (f *fakeTransport) call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	f.calls = append(f.calls, method)
	if r, ok := f.results[method]; ok {
		return r, nil
	}
	return json.RawMessage(`{}`), nil
}
func (f *fakeTransport) notify(context.Context, string) error { return nil }
func (f *fakeTransport) close()                               {}

func runShim(t *testing.T, step Step, tr transport) []*pluginv1.ApplyResponse {
	t.Helper()
	var buf bytes.Buffer
	if err := Run(context.Background(), emitter{w: &buf}, step, tr); err != nil {
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

var toolsList = json.RawMessage(`{"tools":[{"name":"echo","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)

// TestRun_RegisterEmitsRung3DerivedContract proves register mode emits each tool as a
// RUNG_DECLARED derived_contract (schema_id mcp/<server>/<tool>.input at the rev) and
// NO write-back (MF-5), ending with a terminal ok (ADR-0053).
func TestRun_RegisterEmitsRung3DerivedContract(t *testing.T) {
	tr := &fakeTransport{results: map[string]json.RawMessage{"tools/list": toolsList}}
	resps := runShim(t, Step{Mode: ModeRegister, Server: "srv", Rev: "2"}, tr)

	var derived *pluginv1.DerivedContract
	var termOk, sawWriteBack bool
	for _, r := range resps {
		if dc := r.GetDerivedContract(); dc != nil {
			derived = dc
		}
		if len(r.GetWriteBack()) > 0 {
			sawWriteBack = true
		}
		if ev := r.GetEvent(); ev.GetTerminal() {
			termOk = ev.GetOk()
		}
	}
	if derived == nil {
		t.Fatal("register must emit a derived_contract per tool")
	}
	if derived.GetRung() != pluginv1.DerivedContract_RUNG_DECLARED {
		t.Fatalf("register derived_contract must be RUNG_DECLARED (rung-3), got %v", derived.GetRung())
	}
	if derived.GetSchemaId() != "mcp/srv/echo.input" || derived.GetRev() != "2" {
		t.Fatalf("schema_id/rev wrong: %s @ %s", derived.GetSchemaId(), derived.GetRev())
	}
	if sawWriteBack {
		t.Fatal("MF-5: register must NEVER emit write-back (MCP is not a Syncer)")
	}
	if !termOk {
		t.Fatal("register must end with a terminal ok")
	}
}

// TestRun_CallDriftRefusedBeforeCall proves the live-drift check refuses a tool whose
// canonical hash ≠ the pin BEFORE tools/call (§1.5 blocking) — no tools/call is made.
func TestRun_CallDriftRefusedBeforeCall(t *testing.T) {
	tr := &fakeTransport{results: map[string]json.RawMessage{"tools/list": toolsList}}
	resps := runShim(t, Step{Mode: ModeCall, Server: "srv", Tool: "echo", PinnedHash: "deadbeef"}, tr)

	for _, m := range tr.calls {
		if m == "tools/call" {
			t.Fatal("a drifted tool must be refused BEFORE tools/call")
		}
	}
	var sawDrift, termOk bool
	for _, r := range resps {
		if ev := r.GetEvent(); ev != nil {
			if ev.GetFields()["kind"] == "schema_drift" {
				sawDrift = true
			}
			if ev.GetTerminal() {
				termOk = ev.GetOk()
			}
		}
	}
	if !sawDrift || termOk {
		t.Fatalf("drift must emit schema_drift + terminal not-ok (drift=%v ok=%v)", sawDrift, termOk)
	}
}

// TestRun_CallInvokesWhenPinMatches proves a matching pin lets the call proceed and
// the tool result folds to a terminal (STATUS_CHANGED, effectful).
func TestRun_CallInvokesWhenPinMatches(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	hash, _, _ := mcpcanon.CanonicalHash(schema)
	tr := &fakeTransport{results: map[string]json.RawMessage{
		"tools/list": toolsList,
		"tools/call": json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"ok"}]}`),
	}}
	resps := runShim(t, Step{Mode: ModeCall, Server: "srv", Tool: "echo", PinnedHash: hash, Arguments: json.RawMessage(`{"q":"x"}`)}, tr)

	var called, changed bool
	for _, m := range tr.calls {
		if m == "tools/call" {
			called = true
		}
	}
	for _, r := range resps {
		if res := r.GetResult(); res.GetStatus() == pluginv1.ItemResult_STATUS_CHANGED {
			changed = true
		}
	}
	if !called {
		t.Fatal("a matching pin must let tools/call proceed")
	}
	if !changed {
		t.Fatal("an effectful tool call must fold STATUS_CHANGED")
	}
}
