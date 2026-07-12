package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// FixtureServer is a minimal stdio MCP server used by the CI conformance
// test AND (verbatim) by the dev-harness e2e declaration — the reference
// the driver's envelope is gated against on every run (scout rider, §1.5).
const FixtureServer = `import json, sys

TOOLS = [{"name": "greet",
          "description": "Greets a name",
          "inputSchema": {"type": "object", "required": ["name"],
                          "properties": {"name": {"type": "string"}},
                          "additionalProperties": False}}]

for line in sys.stdin:
    try:
        msg = json.loads(line)
    except ValueError:
        continue
    m = msg.get("method")
    if m == "initialize":
        out = {"jsonrpc": "2.0", "id": msg["id"], "result": {
            "protocolVersion": "2025-06-18", "capabilities": {"tools": {}},
            "serverInfo": {"name": "demo-tools", "version": "1"}}}
    elif m == "notifications/initialized":
        continue
    elif m == "tools/list":
        out = {"jsonrpc": "2.0", "id": msg["id"], "result": {"tools": TOOLS}}
    elif m == "tools/call":
        name = msg["params"]["arguments"].get("name", "?")
        out = {"jsonrpc": "2.0", "id": msg["id"], "result": {
            "content": [{"type": "text", "text": "hello " + name}], "isError": False}}
    elif "id" in msg:
        out = {"jsonrpc": "2.0", "id": msg["id"],
               "error": {"code": -32601, "message": "no such method"}}
    else:
        continue
    sys.stdout.write(json.dumps(out) + "\n")
    sys.stdout.flush()
`

// greetSchema mirrors the fixture's declared inputSchema.
const greetSchema = `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}},"additionalProperties":false}`

func testActuator(t *testing.T, pinHash string) Actuator {
	t.Helper()
	return Actuator{
		DefaultImage: "stratt-ee-mcp:test",
		Server: func(_ context.Context, name string) (types.MCPServer, error) {
			if name != "demo-tools" {
				return types.MCPServer{}, fmt.Errorf("mcp server %s not declared", name)
			}
			return types.MCPServer{Name: "demo-tools", Transport: types.MCPTransportStdio, Rev: 1, Script: FixtureServer}, nil
		},
		Pin: func(_ context.Context, name string, version int) (types.Contract, bool, error) {
			if pinHash == "" || name != ContractName("demo-tools", "greet") || version != 1 {
				return types.Contract{}, false, nil
			}
			return types.Contract{Name: name, Version: version, Hash: pinHash, Schema: []byte(greetSchema)}, true, nil
		},
	}
}

func TestPrepareGates(t *testing.T) {
	hash, _, err := CanonicalHash([]byte(greetSchema))
	if err != nil {
		t.Fatal(err)
	}
	a := testActuator(t, hash)

	// Unpinned tool: refused with the register pointer (§2.2 rung 3).
	if _, err := a.Prepare(json.RawMessage(`{"server":"demo-tools","tool":"other"}`), nil); err == nil ||
		!strings.Contains(err.Error(), "register") {
		t.Fatalf("unpinned tool must be refused: %v", err)
	}
	// Arguments violating the pinned schema: refused at the door (§1.5).
	if _, err := a.Prepare(json.RawMessage(`{"server":"demo-tools","tool":"greet","arguments":{"bogus":1}}`), nil); err == nil ||
		!strings.Contains(err.Error(), "contract") {
		t.Fatalf("invalid arguments must be refused with the contract named: %v", err)
	}
	// Undeclared server: refused.
	if _, err := a.Prepare(json.RawMessage(`{"server":"nope","mode":"register"}`), nil); err == nil {
		t.Fatalf("undeclared server must be refused")
	}

	// Valid call: step.json carries the pin, files carry the declared source.
	spec, err := a.Prepare(json.RawMessage(`{"server":"demo-tools","tool":"greet","arguments":{"name":"stratt"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != "stratt-ee-mcp:test" || spec.Files["project/server.py"] != FixtureServer {
		t.Fatalf("spec must carry the image and the declared source verbatim")
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(spec.Files["project/step.json"]), &step); err != nil {
		t.Fatal(err)
	}
	if step["pinnedHash"] != hash || step["rev"] != float64(1) || step["tool"] != "greet" {
		t.Fatalf("step.json pin material wrong: %v", step)
	}
}

// runDriver executes the real driver against the fixture server and feeds
// every stdout line through Interpret — the CI conformance check.
func runDriver(t *testing.T, step map[string]any) []actuators.Interpreted {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	stepDoc, _ := json.Marshal(step)
	for name, content := range map[string]string{
		"driver.py": driverPy, "server.py": FixtureServer, "step.json": string(stepDoc),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", filepath.Join(dir, "driver.py"))
	cmd.Env = append(os.Environ(), "STRATT_DRIVER_BASE="+dir)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var events []actuators.Interpreted
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if iv, ok := (Actuator{}).Interpret(sc.Bytes()); ok {
			events = append(events, iv)
		} else {
			t.Fatalf("driver emitted an uninterpretable line: %s", sc.Text())
		}
	}
	_ = cmd.Wait()
	return events
}

func find(events []actuators.Interpreted, kind string) *actuators.Interpreted {
	for i := range events {
		if events[i].Event.Kind == kind {
			return &events[i]
		}
	}
	return nil
}

// TestCanonicalHashParityAdversarial pins the cross-language canonical form
// over the shapes that historically diverge: HTML-special characters (Go's
// default escaping), non-ASCII (Python's default ensure_ascii), key order,
// and nesting (guardian on ADR-0022).
func TestCanonicalHashParityAdversarial(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	for _, schema := range []string{
		greetSchema,
		`{"b":"<>&","a":"café ☕"}`,
		`{"desc":"a&b<c>d","nested":{"z":[1,2.5,"x"],"a":{"y":true,"x":null}}}`,
		`{"unicode":"日本語","emoji":"🔒"}`,
	} {
		goHash, _, err := CanonicalHash([]byte(schema))
		if err != nil {
			t.Fatalf("go hash of %s: %v", schema, err)
		}
		py := `import json,hashlib,sys
doc=json.dumps(json.load(sys.stdin),sort_keys=True,separators=(",",":"),ensure_ascii=False)
print(hashlib.sha256(doc.encode("utf-8")).hexdigest())`
		cmd := exec.Command("python3", "-c", py)
		cmd.Stdin = strings.NewReader(schema)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("python hash: %v", err)
		}
		if pyHash := strings.TrimSpace(string(out)); pyHash != goHash {
			t.Fatalf("canonical parity broken for %s: go=%s py=%s", schema, goHash, pyHash)
		}
	}
}

func TestDriverConformanceRegister(t *testing.T) {
	events := runDriver(t, map[string]any{
		"mode": "register", "server": "demo-tools", "rev": 1, "transport": "stdio",
	})
	toolsEv := find(events, "mcp_tools")
	if toolsEv == nil || len(toolsEv.MCPTools) != 1 {
		t.Fatalf("register must declare the tools: %+v", events)
	}
	decl := toolsEv.MCPTools[0]
	if decl.Server != "demo-tools" || decl.Rev != 1 || decl.Tool != "greet" {
		t.Fatalf("tool decl: %+v", decl)
	}
	// Cross-language canonical-hash parity: the driver's Python hash must
	// equal Go's over the same schema — the pin the whole design rests on.
	goHash, _, err := CanonicalHash(decl.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if decl.Hash != goHash {
		t.Fatalf("canonical hash parity broken: driver %s, go %s", decl.Hash, goHash)
	}
	fin := find(events, "mcp_finished")
	if fin == nil || fin.Result == nil || fin.Result.Status != actuators.StatusOK {
		t.Fatalf("register must finish ok: %+v", fin)
	}
}

func TestDriverConformanceCallAndDrift(t *testing.T) {
	hash, _, err := CanonicalHash([]byte(greetSchema))
	if err != nil {
		t.Fatal(err)
	}
	// Matching pin: the call goes through.
	events := runDriver(t, map[string]any{
		"mode": "call", "server": "demo-tools", "rev": 1, "transport": "stdio",
		"tool": "greet", "arguments": map[string]any{"name": "stratt"}, "pinnedHash": hash,
	})
	res := find(events, "tool_result")
	if res == nil {
		t.Fatalf("no tool_result: %+v", events)
	}
	content, _ := res.Event.Payload["content"].(json.RawMessage)
	if !strings.Contains(string(content), "hello stratt") {
		t.Fatalf("tool_result content: %s", content)
	}
	fin := find(events, "mcp_finished")
	if fin == nil || fin.Result == nil || fin.Result.Status != actuators.StatusChanged {
		t.Fatalf("successful call must report changed: %+v", fin)
	}

	// Call mode must never carry pin material — pinning is a deliberate
	// register act, not a side effect (guardian on ADR-0022).
	if ev := find(events, "mcp_tools"); ev == nil || len(ev.MCPTools) != 0 {
		t.Fatalf("call mode must list names only, no schema decls: %+v", ev)
	}

	// Stale pin: the driver refuses BEFORE tools/call (§1.5 drift blocks).
	events = runDriver(t, map[string]any{
		"mode": "call", "server": "demo-tools", "rev": 1, "transport": "stdio",
		"tool": "greet", "arguments": map[string]any{"name": "stratt"}, "pinnedHash": "stale",
	})
	drift := find(events, "schema_drift")
	if drift == nil || drift.Result == nil || !drift.Result.Failed {
		t.Fatalf("drift must fail the target: %+v", events)
	}
	if drift.Event.Payload["expected"] != "stale" || drift.Event.Payload["actual"] != hash {
		t.Fatalf("both hashes must be on the record (§1.8): %+v", drift.Event.Payload)
	}
	if find(events, "tool_result") != nil {
		t.Fatalf("drift must block the call itself")
	}
}
