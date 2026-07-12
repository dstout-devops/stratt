package types

import "time"

// MCPCall is one platform-MCP-server tool invocation — the unit of the
// charter §1.6 per-identity usage accounting (ADR-0021).
type MCPCall struct {
	Principal     string `json:"principal"`
	PrincipalKind string `json:"principalKind,omitempty"`
	Tool          string `json:"tool"`
	OK            bool   `json:"ok"`
	DurationMS    int64  `json:"durationMs"`
}

// UsageEntry is the per-(Principal, tool) aggregate served by GET /usage.
type UsageEntry struct {
	Principal     string    `json:"principal"`
	PrincipalKind string    `json:"principalKind,omitempty"`
	Tool          string    `json:"tool"`
	Calls         int64     `json:"calls"`
	Errors        int64     `json:"errors"`
	LastCall      time.Time `json:"lastCall"`
}
