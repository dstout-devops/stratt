package types

// Intent kinds (charter §2.4). v1 ships Intent/Application only; the
// Certificate/FileSet/Access kinds are Phase-3 GA (§8).
const (
	IntentApplication = "Intent/Application"
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
