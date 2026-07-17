// Package mcp is the SHARED canonicalization seam for MCP rung-3 Contract pinning
// (ADR-0022/0053). Both the CORE (authoritative — it recomputes and pins) and the
// stratt-mcp shim (defense-in-depth — its live-drift pre-flight) import THIS ONE
// implementation, so their canonical forms are identical BY CONSTRUCTION (ADR-0053
// MF-4 — no cross-artifact hash divergence, which would permanently block legitimate
// tools). Pure stdlib; the lean SDK gains no dependency.
package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ContractName is the rung-3 pin name for one server tool: mcp/<server>/<tool>.input.
func ContractName(server, tool string) string {
	return "mcp/" + server + "/" + tool + ".input"
}

// CanonicalHash canonicalizes a JSON-Schema document (Go's json sorts map keys;
// HTML-escaping OFF so <, >, & and non-ASCII match the Python shim's
// ensure_ascii=False form) and returns its sha256 hex plus the canonical bytes.
// Any producer/consumer divergence fails SAFE — the call is refused, visibly.
func CanonicalHash(raw json.RawMessage) (string, json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", nil, fmt.Errorf("mcp: schema is not valid JSON: %w", err)
	}
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
