package types

// Intent kinds (charter §2.4). Intent/Application shipped in Phase 2;
// Intent/Certificate is the Phase-3 promote-flagship GA (§8, ADR-0030). The
// FileSet/Access kinds follow. Each kind has a registered spec schema
// (contracts/intents/<kind>.schema.json) — an Intent kind is "implemented"
// exactly when its schema exists (§1.1).
const (
	IntentApplication = "Intent/Application"
	IntentCertificate = "Intent/Certificate"
)

// onRemove lifecycle values (charter §2.4): what happens to compiled state
// when the Intent (or its Assignment) is withdrawn. v1 implements `retain`
// (leave compiled state, raise an orphan Finding — never silent); `revert`
// and `remove` carry domain-specific removal semantics in the kind schema
// and land with the Phase-3 kinds.
const (
	OnRemoveRetain = "retain"
	OnRemoveRevert = "revert"
	OnRemoveRemove = "remove"
)

// Intent is a small declarative document of *what* (charter §2.4): the
// team-facing surface. It carries no targeting — an Assignment binds it to a
// View. CaC-only, like every desired-state declaration (§1.2).
type Intent struct {
	Name string `json:"name"`
	// Kind is the payload kind (v1: Intent/Application). Each kind has a
	// schema driving forms/validation.
	Kind string `json:"kind"`
	// Spec is the kind's payload (e.g. {package, channel} for Application) —
	// referenced by Blueprint observe/remediate templates via explicit field
	// lookup ({{.spec.package}}), never an expression language (non-goal).
	Spec map[string]any `json:"spec,omitempty"`
	// OnRemove is the withdrawal lifecycle (retain|revert|remove, default
	// retain). Withdrawn-but-retained state always raises an orphan Finding
	// (§2.4, §4.3).
	OnRemove string `json:"onRemove,omitempty"`
}
