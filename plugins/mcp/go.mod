// The stratt-mcp shim (ADR-0053) — the MCP client PROTOCOL extracted from the in-tree
// core/internal/actuators/mcp into an EE-Job shim. It runs the (untrusted, Git-declared)
// MCP server in the sandboxed pod (stdio subprocess, §7.3) or reaches it over HTTP,
// speaks JSON-RPC (initialize / tools/list / tools/call), and emits the sovereign
// port's typed shapes: register → each tool schema as a rung-3 derived_contract; call →
// the tool result as an ItemResult. The CORE pins + governs; this shim speaks MCP.
module github.com/dstout-devops/stratt/plugins/mcp

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.82.1 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
