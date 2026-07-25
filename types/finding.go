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

// Finding launch kinds (ADR-0120 D1): what a Finding's LaunchWorkflow DOES. A closed,
// core-owned set, because these are SPINE acts — converge live state, retire abandoned
// state, create declared state. A plugin never adds one; that is what keeps the field from
// becoming an extension point (§1.4), and it is why the set stays small enough to enumerate
// rather than growing a field per act (which is what it replaced).
//
// A fourth member must argue its way in. ADR-0114's decommission path is the first real
// test: if it needs `decommission` rather than reusing `remove`, this set was drawn wrong.
const (
	// LaunchRemediate converges live state to its compiled expectation.
	LaunchRemediate = "remediate"
	// LaunchRemove retires state the estate no longer declares (an orphan's withdrawal).
	// `remove` and not `withdraw`: the chain already reads onRemove: remove →
	// Blueprint.removeWorkflow → Baseline.RemoveParams, and a frozen vocabulary does not
	// get a synonym for an act it already names (§2).
	LaunchRemove = "remove"
	// LaunchBuild creates state the estate declares but which does not exist yet — the
	// gated provisioning build (§5 Flow 1: never auto-run).
	LaunchBuild = "build"
)

// ValidLaunchKind reports whether k is a known launch act. Empty is valid and means "this
// Finding's spec lives on its Baseline", which is the common case.
func ValidLaunchKind(k string) bool {
	switch k {
	case "", LaunchRemediate, LaunchRemove, LaunchBuild:
		return true
	}
	return false
}

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
	// LaunchWorkflow, LaunchParams and LaunchKind are this Finding's OWN launch spec —
	// the Workflow that resolves it and the inputs to pass (ADR-0120 D1). Empty on a
	// Finding whose spec lives on its Baseline.
	//
	// A Finding carries its own spec exactly when no Baseline can hold it, which is two of
	// the three cases and for different permanent reasons:
	//
	//   - ORPHAN: the Baseline existed and is PRUNED by the same Apply that wrote the
	//     Finding, because a Baseline whose Assignment is withdrawn must stop being
	//     observed. ADR-0118 refused to copy launch params onto Findings — "a copy would be
	//     a second, staleable record of a Git-derived fact for no gain" — and that holds
	//     only while the Baseline exists. Here the copy is the ONLY record.
	//   - PROVISION: there was never a Baseline. `provision/<intent>` is a synthetic
	//     grouping name, not a row; a real Baseline would be a compiled expectation over
	//     something that does not exist yet, which ADR-0058 M1 and §1.2 both refuse.
	//
	// THE TWO ARE NOT THE SAME KIND OF COPY, and confusing them is a §1.2 bug. An orphan's
	// spec is IMMUTABLE — nothing else remembers it, so it is written once. A provision
	// Finding's is DERIVED from Git and must be REDERIVED every reconcile, or an already-open
	// Finding keeps serving its first pass's values and a later edit to labels/placement
	// launches yesterday's desired state (see graph.WriteProvisionFinding's DO UPDATE).
	LaunchWorkflow string         `json:"launchWorkflow,omitempty"`
	LaunchParams   map[string]any `json:"launchParams,omitempty"`
	// LaunchKind names the ACT, and is the SINGLE branch point for what launches.
	//
	// Framework used to double as one — it carries "orphan" and "provision", and the launch
	// door branched on `Framework == "orphan"`. Two discriminators that can disagree, with
	// the winner decided by whichever branch ran first, is the kind of implicit resolution
	// §2.4 forbids. So Framework reverts to what §2.4 calls it — the compliance tag, "one
	// kind, framework-tagged" — and this field decides.
	LaunchKind string `json:"launchKind,omitempty"`
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
