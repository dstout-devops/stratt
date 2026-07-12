package types

// MCPServer transports. MCP is a transport beneath the sovereign contract
// (charter §1.5) — inadmissible for Syncers (§2.2), admissible for
// Actuators/Actions at derivation rung 3 (mcp-declared-and-pinned).
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
)

// MCPTokenRef points at the CredentialRef key holding an http server's
// bearer token — a pointer, never material (§2.5). The Step invoking the
// server must carry the named CredentialRef so the kubelet mounts it.
type MCPTokenRef struct {
	CredentialRef string `json:"credentialRef"`
	Key           string `json:"key"`
}

// MCPServer is a CaC-declared external MCP server the `mcp` Actuator may
// invoke (charter §2.3, ADR-0022). For stdio transport the declaration
// carries the server's entire source: the sandbox runs exactly what Git
// review approved and nothing else — the command can never derive from
// Principal or Run-time input (the structural mitigation for the MCP stdio
// injection class; dependency-scout mandate).
type MCPServer struct {
	Name string `json:"name"`
	// Transport is stdio (in-pod subprocess, sandboxed) or http (remote
	// Streamable-HTTP endpoint).
	Transport string `json:"transport"`
	// Rev keys this declaration's pinned tool Contracts
	// (mcp/<name>/<tool>.input @ version=rev). Accepting a changed tool
	// schema is a deliberate Git act: bump rev and re-register (§1.5:
	// drift within a rev is blocking).
	Rev int `json:"rev"`
	// Script is the stdio server's Python source (Git-reviewed, mounted
	// read-only, run verbatim).
	Script string `json:"script,omitempty"`
	// Endpoint is the http server's URL.
	Endpoint string `json:"endpoint,omitempty"`
	// TokenRef optionally names the bearer credential for http servers.
	TokenRef *MCPTokenRef `json:"tokenRef,omitempty"`
}
