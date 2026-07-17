// Package mcp is the MCP-client content-expertise, extracted from the in-tree
// core/internal/actuators/mcp into the EE-image shim (ADR-0053). It speaks JSON-RPC
// 2.0 to an MCP server (stdio subprocess — the sandboxed, Git-declared server — or
// HTTP) and maps the two modes onto the sovereign port's typed shapes. No graph
// write path, no core dependency; the shim proposes tool schemas, the hub pins them.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	mcpcanon "github.com/dstout-devops/stratt/sdk/mcp"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Modes.
const (
	ModeRegister = "register"
	ModeCall     = "call"
)

// Step is what the shim reads from the Job content — the core-resolved MCP operation
// (the core resolved the server declaration + rev + pin; the shim never reads the
// graph). transport is stdio|http; script is the Git-reviewed stdio server source.
type Step struct {
	Mode       string          `json:"mode"`
	Server     string          `json:"server"`
	Rev        string          `json:"rev"`
	Transport  string          `json:"transport"`
	Script     string          `json:"script,omitempty"`
	Endpoint   string          `json:"endpoint,omitempty"`
	TokenFile  string          `json:"tokenFile,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	PinnedHash string          `json:"pinnedHash,omitempty"`
}

// transport is the JSON-RPC round-trip against the server.
type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string) error
	close()
}

// Emitter writes typed port shapes.
type emitter struct{ w io.Writer }

func (e emitter) emit(r *pluginv1.ApplyResponse) {
	if b, err := protojson.Marshal(r); err == nil {
		_, _ = e.w.Write(b)
		_, _ = e.w.Write([]byte("\n"))
	}
}

func (e emitter) diag(server, msg string) {
	e.emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: msg, At: timestamppb.Now(),
		Fields: map[string]string{"kind": "diagnostic", "host": server},
	}})
}

func (e emitter) phase(server, phase string) {
	e.emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: phase, At: timestamppb.Now(),
		Fields: map[string]string{"kind": "phase", "host": server},
	}})
}

// terminal emits the required terminal + the per-server ItemResult; the hub folds
// Succeeded (MF5/F-1). A call is presumed effectful (CHANGED); register/failure map
// straight to OK/FAILED.
func (e emitter) terminal(server string, ok, changed bool, msg string) {
	status := pluginv1.ItemResult_STATUS_OK
	switch {
	case !ok:
		status = pluginv1.ItemResult_STATUS_FAILED
	case changed:
		status = pluginv1.ItemResult_STATUS_CHANGED
	}
	e.emit(&pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: server, Status: status}})
	e.emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg,
		Fields: map[string]string{"host": server},
	}})
}

// Execute is the production entry (the cmd calls it): open the transport, run the
// handshake + the mode, emit typed shapes to w. Injectable transport for tests.
func Execute(ctx context.Context, w io.Writer, dir string, step Step) error {
	e := emitter{w: w}
	tr, err := openTransport(ctx, e, dir, step)
	if err != nil {
		e.diag(step.Server, "transport: "+err.Error())
		e.terminal(step.Server, false, false, "transport failed")
		return nil
	}
	defer tr.close()
	return Run(ctx, e, step, tr)
}

// Run drives the handshake, tools/list, and the mode against tr, emitting the port's
// typed shapes: register → a rung-3 derived_contract per tool (MF-5: no write-back);
// call → drift-check then tools/call → the result (ADR-0053).
func Run(ctx context.Context, e emitter, step Step, tr transport) error {
	e.phase(step.Server, "initialize")
	if _, err := tr.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "stratt-mcp", "version": "1"},
	}); err != nil {
		e.diag(step.Server, "initialize error: "+err.Error())
		e.terminal(step.Server, false, false, "initialize failed")
		return nil
	}
	_ = tr.notify(ctx, "notifications/initialized")

	listed, err := tr.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		e.diag(step.Server, "tools/list error: "+err.Error())
		e.terminal(step.Server, false, false, "tools/list failed")
		return nil
	}
	tools, err := parseTools(listed)
	if err != nil {
		e.diag(step.Server, "tools/list: "+err.Error())
		e.terminal(step.Server, false, false, "tools/list decode failed")
		return nil
	}

	if step.Mode == ModeRegister {
		// Rung-3 derived_contract per tool (the pin material). Emitted ONLY in register
		// mode (MF-3 gate i); the CORE recomputes the canonical hash + pins at its own
		// held rev (MF-2/MF-4). NO write-back (MF-5 — MCP is not a Syncer).
		for _, t := range tools {
			e.emit(&pluginv1.ApplyResponse{DerivedContract: &pluginv1.DerivedContract{
				Rung:     pluginv1.DerivedContract_RUNG_DECLARED,
				SchemaId: mcpcanon.ContractName(step.Server, t.Name),
				Rev:      step.Rev,
				Schema:   t.InputSchema,
			}})
		}
		e.terminal(step.Server, true, false, fmt.Sprintf("registered %d tool(s)", len(tools)))
		return nil
	}

	// call mode.
	var tool *toolDecl
	for i := range tools {
		if tools[i].Name == step.Tool {
			tool = &tools[i]
			break
		}
	}
	if tool == nil {
		e.diag(step.Server, "server no longer declares tool "+step.Tool)
		e.terminal(step.Server, false, false, "tool not found")
		return nil
	}
	// Defense-in-depth live-drift check BEFORE tools/call (the core pinned + validated
	// too): refuse a drifted tool, both hashes on the record (§1.5 blocking, §1.8).
	if step.PinnedHash != "" {
		actual, _, herr := mcpcanon.CanonicalHash(tool.InputSchema)
		if herr != nil || actual != step.PinnedHash {
			e.emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
				Level: pluginv1.TaskEvent_LEVEL_ERROR, At: timestamppb.Now(), Message: "schema drift",
				Fields: map[string]string{"host": step.Server, "kind": "schema_drift", "expected": step.PinnedHash, "actual": actual},
			}})
			e.terminal(step.Server, false, false, "schema drift — refused before call")
			return nil
		}
	}
	args := step.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	result, err := tr.call(ctx, "tools/call", map[string]any{"name": step.Tool, "arguments": args})
	if err != nil {
		e.diag(step.Server, "tools/call error: "+err.Error())
		e.terminal(step.Server, false, false, "tools/call failed")
		return nil
	}
	isErr := toolIsError(result)
	e.emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Message: "tool result",
		Fields: map[string]string{"host": step.Server, "kind": "tool_result", "isError": fmt.Sprintf("%v", isErr)},
	}})
	e.terminal(step.Server, !isErr, !isErr, "tool call complete")
	return nil
}

type toolDecl struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func parseTools(result json.RawMessage) ([]toolDecl, error) {
	var r struct {
		Tools []toolDecl `json:"tools"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, err
	}
	for i := range r.Tools {
		if len(r.Tools[i].InputSchema) == 0 {
			r.Tools[i].InputSchema = json.RawMessage(`{}`)
		}
	}
	return r.Tools, nil
}

func toolIsError(result json.RawMessage) bool {
	var r struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(result, &r)
	return r.IsError
}

// openTransport opens stdio (spawn the Git-declared server, sandboxed) or http.
func openTransport(ctx context.Context, e emitter, dir string, step Step) (transport, error) {
	if step.Transport == "stdio" {
		return newStdio(ctx, e, dir, step)
	}
	return newHTTP(e, step)
}

// rpcEnvelope builds a JSON-RPC 2.0 request/notification.
func rpcEnvelope(id int, method string, params any) map[string]any {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	return m
}

// rpcResult unwraps a JSON-RPC response, surfacing an `error` member as a Go error.
func rpcResult(raw []byte) (json.RawMessage, error) {
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if len(resp.Error) > 0 {
		return nil, fmt.Errorf("jsonrpc error: %s", truncate(string(resp.Error), 400))
	}
	return resp.Result, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ── stdio transport ──────────────────────────────────────────────────────────

type stdioTransport struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *bytes.Buffer
	e      emitter
	server string
	id     int
}

func newStdio(ctx context.Context, e emitter, dir string, step Step) (transport, error) {
	// The server source is the Git-reviewed declaration, run verbatim — never a
	// command from run-time input (ADR-0022). It runs in THIS sandboxed pod.
	if err := os.WriteFile(dir+"/server.py", []byte(step.Script), 0o644); err != nil {
		return nil, err
	}
	e.phase(step.Server, "spawn stdio server")
	cmd := exec.CommandContext(ctx, "python3", dir+"/server.py")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr // surfaced as diagnostics if the server dies (§1.8)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stdioTransport{cmd: cmd, in: stdin, out: bufio.NewReaderSize(stdout, 1<<20), stderr: &stderr, e: e, server: step.Server}, nil
}

func (s *stdioTransport) send(v any) error {
	b, _ := json.Marshal(v)
	_, err := s.in.Write(append(b, '\n'))
	return err
}

func (s *stdioTransport) call(_ context.Context, method string, params any) (json.RawMessage, error) {
	s.id++
	want := s.id
	if err := s.send(rpcEnvelope(want, method, params)); err != nil {
		return nil, err
	}
	for {
		line, err := s.out.ReadBytes('\n')
		if err != nil {
			s.surfaceStderr()
			return nil, fmt.Errorf("stdio server closed the pipe")
		}
		var probe struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(line), &probe) != nil || probe.ID == nil {
			continue // server-initiated request/notification — out of scope, ignored
		}
		if *probe.ID == want && (len(probe.Result) > 0 || len(probe.Error) > 0) {
			return rpcResult(bytes.TrimSpace(line))
		}
	}
}

func (s *stdioTransport) notify(_ context.Context, method string) error {
	return s.send(rpcEnvelope(0, method, nil))
}

func (s *stdioTransport) surfaceStderr() {
	tail := s.stderr.String()
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	for _, l := range strings.Split(tail, "\n") {
		if strings.TrimSpace(l) != "" {
			s.e.diag(s.server, "server stderr: "+l)
		}
	}
}

func (s *stdioTransport) close() {
	_ = s.in.Close()
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
}

// ── http transport ───────────────────────────────────────────────────────────

type httpTransport struct {
	client    *http.Client
	endpoint  string
	headers   map[string]string
	sessionID string
	id        int
}

func newHTTP(e emitter, step Step) (transport, error) {
	h := &httpTransport{
		client:   &http.Client{Timeout: 30 * time.Second},
		endpoint: step.Endpoint,
		headers:  map[string]string{"Accept": "application/json, text/event-stream", "Content-Type": "application/json"},
	}
	if step.TokenFile != "" {
		tok, err := os.ReadFile(step.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("token file unreadable: %w", err)
		}
		h.headers["Authorization"] = "Bearer " + strings.TrimSpace(string(tok))
	}
	return h, nil
}

func (h *httpTransport) post(ctx context.Context, env map[string]any) ([]byte, string, error) {
	body, _ := json.Marshal(env)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.sessionID = sid
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("http %d", resp.StatusCode)
	}
	return raw, resp.Header.Get("Content-Type"), nil
}

func (h *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	h.id++
	want := h.id
	raw, ctype, err := h.post(ctx, rpcEnvelope(want, method, params))
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(ctype, "text/event-stream") {
		for _, line := range strings.Split(string(raw), "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var probe struct {
					ID *int `json:"id"`
				}
				if json.Unmarshal([]byte(data), &probe) == nil && probe.ID != nil && *probe.ID == want {
					return rpcResult([]byte(data))
				}
			}
		}
		return nil, fmt.Errorf("no matching SSE response")
	}
	return rpcResult(raw)
}

func (h *httpTransport) notify(ctx context.Context, method string) error {
	_, _, err := h.post(ctx, rpcEnvelope(0, method, nil))
	return err
}

func (h *httpTransport) close() {}
