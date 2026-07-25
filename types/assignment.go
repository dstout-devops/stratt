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
	// Environments scopes the Assignment (prod, staging, …) — a membership filter,
	// never a router and never a precedence axis (ADR-0057 D2).
	//
	// Note it is a SET: one Assignment spanning [staging, prod] carries ONE set of
	// Values. Varying a value per environment therefore means one Assignment per
	// environment, which is the intended shape — the Assignment IS the per-environment
	// instantiation of a universal Intent.
	Environments []string `json:"environments,omitempty"`
	// Values are this Assignment's parameter DECLARATIONS, merged with the Intent's spec
	// and the Blueprint's defaults into the effective spec that routes substitute
	// {{.spec.X}} from (ADR-0118 D1, discharging ADR-0083 D1's "plus optional overrides").
	//
	// The rule that keeps this from becoming a precedence ladder (§2.4, the anti-GPO
	// axiom): Values and the Intent's spec are CO-EQUAL declarations, so a path set by
	// BOTH is an exclusive double-claim and FAILS THE COMPILE, naming both. Only the
	// Blueprint's defaults yield. So to decide a value per environment, the Intent must
	// OMIT it — which is the honest expression of "this is an environment-level decision"
	// rather than a silent override of a fleet-level one. Lists are the additive claim and
	// union instead, meaning no layer can narrow one.
	//
	// Environment-KEYED maps here (values: {prod: {…}, staging: {…}}) are FORBIDDEN —
	// see EnvScoped in envscope.go: env-conditional config values are the
	// new-configuration-language non-goal. Scope by declaring one Assignment per
	// environment instead.
	Values map[string]any `json:"values,omitempty"`
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
