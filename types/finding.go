package types

import (
	"encoding/json"
	"time"
)

// Finding statuses (charter §2.4, §4.3 flap damping). pending = drifted but
// not yet N consecutive observations; open = fired; resolved = a clean
// observation closed it (kept as the audit record).
const (
	FindingPending  = "pending"
	FindingOpen     = "open"
	FindingResolved = "resolved"
)

// Finding is a drift/compliance result (charter §2.4): Entity + Baseline +
// observed-vs-expected diff + severity + Evidence ref. One kind, framework-
// tagged. v1 Evidence is the redacted diff snapshot plus the Run ref; the
// object-locked Evidence store is Phase 3.
type Finding struct {
	ID       string `json:"id"`
	Baseline string `json:"baseline"`
	// Target is the check Run's per-target name the drift was observed on.
	Target string `json:"target"`
	// EntityID resolves Target through the View's membership when the
	// target names an Entity; empty when the target is not an Entity (e.g.
	// an opentofu workspace).
	EntityID string `json:"entityId,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	// Framework is the Baseline's compliance tag (§2.4: framework-tagged).
	Framework string `json:"framework,omitempty"`
	// ConsecutiveDrifted counts drifted observations since the last clean
	// one — the §4.3 damping counter, visible, never hidden.
	ConsecutiveDrifted int `json:"consecutiveDrifted"`
	// Diff is the latest observed-vs-expected detail (redacted upstream,
	// size-capped with visible truncation).
	Diff json.RawMessage `json:"diff,omitempty"`
	// RunID is the Evidence ref: the check Run that made the latest
	// observation (§1.8 descent: Finding → Run → task events).
	RunID         string     `json:"runId,omitempty"`
	FirstObserved time.Time  `json:"firstObserved"`
	LastObserved  time.Time  `json:"lastObserved"`
	OpenedAt      *time.Time `json:"openedAt,omitempty"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
}
