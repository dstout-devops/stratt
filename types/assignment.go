package types

// Assignment binds an Intent to a View, per environment/ring (charter §2.4),
// pinning a Blueprint version. Kept separate from the Intent so one Intent
// can be assigned differently across environments and patch rings. CaC-only:
// an Assignment references a View that MUST be cac-declared — otherwise
// desired state escapes Git (§2.1 guardian constraint).
type Assignment struct {
	Name string `json:"name"`
	// Intent names the declared Intent this Assignment targets.
	Intent string `json:"intent"`
	// View is the cac-declared View naming the target Entity set.
	View string `json:"view"`
	// Blueprint + BlueprintVersion pin the composition that compiles this
	// Assignment (Assignments pin a Blueprint version, §2.4).
	Blueprint        string `json:"blueprint"`
	BlueprintVersion int    `json:"blueprintVersion"`
	// Environments scopes the Assignment (prod, staging, …) — recorded;
	// per-environment routing is a Blueprint concern.
	Environments []string `json:"environments,omitempty"`
	// MaxDelta overrides the engine max-delta fraction for this Assignment
	// (§4.3): if the compiled target set changes by more than this fraction
	// of the previous set between reconciles, the compile pauses pending
	// acknowledgement. Nil uses the engine default.
	MaxDelta *float64 `json:"maxDelta,omitempty"`
	// AckDelta acknowledges a paused large membership delta (§4.3). Bumping
	// it in Git is the deliberate act that unblocks one over-threshold
	// compile — never a priority or last-writer field (anti-GPO, §2.4).
	AckDelta int `json:"ackDelta,omitempty"`
}
