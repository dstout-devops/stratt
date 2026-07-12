package types

// Contract derivation-ladder rungs (charter §2.2). v1 ships only the top
// rung; tool-derived (tofu plan JSON, OpenAPI import) and
// MCP-declared-and-pinned arrive with their Actuators/adapters.
const (
	RungHandWritten = "hand-written"
	RungToolDerived = "tool-derived"
	RungMCPDeclared = "mcp-declared"
)

// Contract is a pinned JSON Schema document on a Step's inputs/outputs or a
// Facet namespace (charter §1.5, §2.2): data, never a language class. The
// Hash pins the exact document bytes; drift against a registered pin is
// blocking (ADR-0015).
type Contract struct {
	// Name is the document path without extension, e.g.
	// "actuators/script.input" or "facets/os.kernel".
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Rung is the derivation-ladder provenance of the schema itself.
	Rung string `json:"rung"`
	// Hash is sha256 over the exact document bytes, hex-encoded.
	Hash string `json:"hash"`
	// Schema is the raw JSON Schema document.
	Schema []byte `json:"schema"`
}
