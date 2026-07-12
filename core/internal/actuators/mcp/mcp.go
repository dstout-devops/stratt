// Package mcp is the `mcp` Actuator (charter §2.3: generic MCP-client) —
// Stratt consuming external MCP servers as sandboxed execution tools
// (ADR-0022). The external server runs inside the EE pod (stdio: the
// Git-declared script, verbatim) or is reached over Streamable HTTP; the
// driver is a hand-rolled JSON-RPC client (dependency-scout: the Python
// MCP SDK's server-stack footprint is wrong for a minimal sandbox image).
//
// Rung 3 (§2.2, mcp-declared-and-pinned): tool input schemas pin as
// Contracts `mcp/<server>/<tool>.input` at the declaration's rev; drift
// within a rev is BLOCKING — enforced twice: arguments validate against the
// pin at Prepare, and the driver hard-fails before tools/call when the live
// schema's canonical hash differs from the control-plane-supplied pin.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/types"
)

// Modes: register pins the server's declared tool schemas; call invokes one
// tool (refused unless its schema is pinned at the declaration's rev).
const (
	ModeRegister = "register"
	ModeCall     = "call"
)

// ContractName is the rung-3 pin name for one server tool.
func ContractName(server, tool string) string {
	return "mcp/" + server + "/" + tool + ".input"
}

// CanonicalHash canonicalizes a schema document (sorted keys, compact —
// Go's json.Marshal sorts map keys) and returns its sha256 hex. The driver
// computes the identical form in Python (json.dumps sort_keys separators);
// any cross-language mismatch fails safe: the call is refused, visibly.
func CanonicalHash(raw json.RawMessage) (string, json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", nil, fmt.Errorf("mcp: schema is not valid JSON: %w", err)
	}
	// Encoder with HTML escaping off: Go must emit the same bytes Python's
	// ensure_ascii=False does for <, >, & and non-ASCII (guardian on
	// ADR-0022 — a divergence would permanently block legitimate tools).
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", nil, err
	}
	canonical := bytes.TrimRight(buf.Bytes(), "\n")
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), canonical, nil
}

// params is the actuator's interpretation of Step params — the Contract is
// contracts/actuators/mcp.input (§1.5).
type params struct {
	Server    string         `json:"server"`
	Mode      string         `json:"mode"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	EEImage   string         `json:"eeImage"`
}

// Actuator implements the mcp Actuator. Lookups are store-backed closures
// wired at startup; Prepare uses them to fetch the declaration and the pin.
type Actuator struct {
	DefaultImage string
	// Server resolves a declared MCPServer by name.
	Server func(ctx context.Context, name string) (types.MCPServer, error)
	// Pin resolves a pinned Contract by (name, version); ok=false when the
	// pin does not exist.
	Pin func(ctx context.Context, name string, version int) (types.Contract, bool, error)
}

// Name implements actuators.Actuator.
func (Actuator) Name() string { return "mcp" }

// Prepare implements actuators.Actuator.
func (a Actuator) Prepare(raw json.RawMessage, _ []actuators.Target) (actuators.JobSpec, error) {
	if a.Server == nil || a.Pin == nil {
		return actuators.JobSpec{}, fmt.Errorf("mcp: actuator not wired (server/pin lookups)")
	}
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("mcp: invalid params: %w", err)
		}
	}
	if p.Mode == "" {
		p.Mode = ModeCall
	}
	// Defense-in-depth behind the Contract seam (§1.5 validates upstream).
	if p.Server == "" || (p.Mode != ModeRegister && p.Mode != ModeCall) {
		return actuators.JobSpec{}, fmt.Errorf("mcp: server and mode (register|call) are required")
	}
	ctx := context.Background()
	server, err := a.Server(ctx, p.Server)
	if err != nil {
		return actuators.JobSpec{}, fmt.Errorf("mcp: %w", err)
	}

	step := map[string]any{
		"mode":      p.Mode,
		"server":    server.Name,
		"rev":       server.Rev,
		"transport": server.Transport,
	}
	if server.Transport == types.MCPTransportHTTP {
		step["endpoint"] = server.Endpoint
		if server.TokenRef != nil {
			// The kubelet mounts the CredentialRef; material never rides
			// here (§2.5). The driver fails visibly if the mount is absent.
			step["tokenFile"] = "/runner/credentials/" + server.TokenRef.CredentialRef + "/" + server.TokenRef.Key
		}
	}

	if p.Mode == ModeCall {
		if p.Tool == "" {
			return actuators.JobSpec{}, fmt.Errorf("mcp: call mode requires tool")
		}
		pin, ok, err := a.Pin(ctx, ContractName(server.Name, p.Tool), server.Rev)
		if err != nil {
			return actuators.JobSpec{}, fmt.Errorf("mcp: %w", err)
		}
		if !ok {
			return actuators.JobSpec{}, fmt.Errorf("mcp: no pinned contract %s at rev %d — run a register Step first (§2.2 rung 3: declared AND pinned)",
				ContractName(server.Name, p.Tool), server.Rev)
		}
		args := json.RawMessage(`{}`)
		if p.Arguments != nil {
			if args, err = json.Marshal(p.Arguments); err != nil {
				return actuators.JobSpec{}, fmt.Errorf("mcp: marshal arguments: %w", err)
			}
		}
		// Contract at the door (§1.5): arguments validate against the pin.
		if err := contract.ValidateDocument(pin.Name, pin.Schema, args); err != nil {
			return actuators.JobSpec{}, fmt.Errorf("mcp: arguments %w", err)
		}
		step["tool"] = p.Tool
		step["arguments"] = json.RawMessage(args)
		step["pinnedHash"] = pin.Hash
	}

	stepDoc, err := json.Marshal(step)
	if err != nil {
		return actuators.JobSpec{}, err
	}
	files := map[string]string{
		"project/driver.py": driverPy,
		"project/step.json": string(stepDoc),
	}
	if server.Transport == types.MCPTransportStdio {
		// The Git-reviewed source, verbatim — never a command from
		// Principal or Run-time input (scout mandate, ADR-0022).
		files["project/server.py"] = server.Script
	}
	image := p.EEImage
	if image == "" {
		image = a.DefaultImage
	}
	return actuators.JobSpec{
		Files:   files,
		Command: []string{"python3", "/runner/project/driver.py"},
		Image:   image,
	}, nil
}

// driverEvent is one wrapped line of the driver's stream.
type driverEvent struct {
	Counter  int64           `json:"counter"`
	Event    string          `json:"event"`
	Phase    string          `json:"phase,omitempty"`
	Server   string          `json:"server,omitempty"`
	Rev      int             `json:"rev,omitempty"`
	Tools    []driverTool    `json:"tools,omitempty"`
	Names    []string        `json:"names,omitempty"`
	Expected string          `json:"expected,omitempty"`
	Actual   string          `json:"actual,omitempty"`
	IsError  *bool           `json:"isError,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	Line     string          `json:"line,omitempty"`
	RC       *int            `json:"rc,omitempty"`
	Mode     string          `json:"mode,omitempty"`
}

type driverTool struct {
	Name        string          `json:"name"`
	Hash        string          `json:"hash"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Interpret implements actuators.Actuator.
func (Actuator) Interpret(line []byte) (actuators.Interpreted, bool) {
	var ev driverEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Counter == 0 {
		return actuators.Interpreted{}, false
	}
	out := actuators.Interpreted{Event: types.RunEvent{Seq: ev.Counter, Kind: ev.Event}}
	if ev.Server != "" {
		out.Event.Target = ev.Server
	}
	switch ev.Event {
	case "phase":
		out.Event.Payload = map[string]any{"phase": ev.Phase}

	case "mcp_tools":
		// Register mode carries the declared schemas — the rung-3 pin
		// material (descriptions stay on this event, inspectable, and are
		// NOT pinned into the Contract documents; §7.3 screening posture).
		// Call mode carries names only: pinning is never a side effect of
		// calling a sibling tool (guardian on ADR-0022).
		names := append([]string{}, ev.Names...)
		for _, t := range ev.Tools {
			out.MCPTools = append(out.MCPTools, actuators.MCPToolDecl{
				Server: ev.Server, Rev: ev.Rev, Tool: t.Name,
				Hash: t.Hash, Schema: t.InputSchema,
			})
			names = append(names, t.Name)
		}
		out.Event.Payload = map[string]any{"tools": names}

	case "schema_drift":
		// The pin check failed inside the sandbox, before tools/call —
		// drift is blocking, and both hashes are on the record (§1.8).
		out.Event.Payload = map[string]any{"expected": ev.Expected, "actual": ev.Actual}
		out.Result = &actuators.TargetResult{Target: ev.Server, Status: actuators.StatusFailed, Failed: true}

	case "tool_result":
		payload := map[string]any{}
		if ev.Content != nil {
			payload["content"] = json.RawMessage(ev.Content)
		}
		if ev.IsError != nil {
			payload["isError"] = *ev.IsError
		}
		out.Event.Payload = payload

	case "mcp_finished":
		rc := 0
		if ev.RC != nil {
			rc = *ev.RC
		}
		out.Event.Payload = map[string]any{"rc": rc, "mode": ev.Mode}
		status := actuators.StatusOK
		if ev.Mode == ModeCall && rc == 0 {
			// An external tool call is presumed effectful.
			status = actuators.StatusChanged
		}
		if rc != 0 {
			status = actuators.StatusFailed
		}
		out.Result = &actuators.TargetResult{Target: ev.Server, Status: status, Failed: rc != 0}

	case "raw":
		out.Event.Payload = map[string]any{"line": ev.Line}

	default:
		return actuators.Interpreted{}, false
	}
	return out, true
}

// FromEnv builds the Actuator from process configuration (strattd wiring).
func FromEnv(server func(context.Context, string) (types.MCPServer, error),
	pin func(context.Context, string, int) (types.Contract, bool, error)) Actuator {
	image := os.Getenv("STRATT_EE_MCP_IMAGE")
	if image == "" {
		image = "stratt-ee-mcp:dev"
	}
	return Actuator{DefaultImage: image, Server: server, Pin: pin}
}
