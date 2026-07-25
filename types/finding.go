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
	// RemoveWorkflow and RemoveParams are the launch spec for RETIRING the abandoned state
	// an ORPHAN Finding reports — the Blueprint's removeWorkflow and the params compiled
	// under the withdrawn Assignment (ADR-0118 D3). Empty on every other Finding.
	//
	// This is the ONE case where a Finding carries its own launch spec instead of reading it
	// from its Baseline. ADR-0118 refused that copy deliberately — "a Finding already
	// references its Baseline, so a copy would be a second, staleable record of a Git-derived
	// fact for no gain" — and that reasoning holds exactly as long as the Baseline exists.
	// For an orphan it does not: Apply writes the orphan Finding and then PRUNES the
	// compiled Baseline, because a Baseline whose Assignment is withdrawn must stop being
	// observed. So this is not a second record of the fact, it is the only one, and without
	// it the values die with the row (§1.8: the fix for abandoned state must remain
	// reachable, and abandoned state is exactly the case where nothing else remembers).
	//
	// Same names as Blueprint.removeWorkflow/removeParams and Baseline.RemoveParams — one
	// concept, one name down the whole chain (§2).
	RemoveWorkflow string         `json:"removeWorkflow,omitempty"`
	RemoveParams   map[string]any `json:"removeParams,omitempty"`
	// RunID is the Evidence ref: the check Run that made the latest
	// observation (§1.8 descent: Finding → Run → task events).
	RunID         string     `json:"runId,omitempty"`
	FirstObserved time.Time  `json:"firstObserved"`
	LastObserved  time.Time  `json:"lastObserved"`
	OpenedAt      *time.Time `json:"openedAt,omitempty"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
	// ResolvedReason distinguishes WHY a Finding resolved (§1.8, ADR-0043):
	// "observed-clean" (the drift went away) vs "entity-tombstoned" (the Entity
	// the Finding was about no longer exists — e.g. a renewed cert). Empty on a
	// live Finding.
	ResolvedReason string `json:"resolvedReason,omitempty"`
}
