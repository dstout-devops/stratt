package types

// Trigger kinds: schedule (ADR-0010, Temporal Schedules) and event
// (ADR-0018, Emitter event × CEL rule).
const (
	TriggerSchedule = "schedule"
	TriggerEvent    = "event"
)

// Trigger is anything that starts a Run (charter §2: Temporal Schedule,
// Emitter event × CEL rule, manual, API/MCP). A schedule Trigger compiles to
// a Temporal Schedule (§3: Temporal owns all lifecycle) whose action starts
// the Run Workflow with this declaration's launch parameters.
//
// Triggers are CaC-only (ADR-0010): Principal names the service identity the
// scheduled Runs execute as, which makes declaring a Trigger an impersonation
// grant — Git review is that grant's authorization. The API surface is
// read-only until View-scoped execution authz lands (Phase 2/3, ADR-0009).
type Trigger struct {
	// Name is the stable reference; the Temporal Schedule id derives from it.
	Name string `json:"name"`
	// Kind is the trigger kind; v1: "schedule".
	Kind string `json:"kind"`
	// Cron is the schedule spec (kind schedule; Temporal validates).
	Cron string `json:"cron,omitempty"`
	// Paused declares the schedule paused without deleting it (drills,
	// maintenance windows).
	Paused bool `json:"paused,omitempty"`
	// Emitter and When belong to kind event (ADR-0018): events from the
	// named Emitter evaluate against the CEL expression When (compiled at
	// declaration parse — a bad expression fails its file, never fires).
	Emitter string `json:"emitter,omitempty"`
	When    string `json:"when,omitempty"`
	// CooldownSeconds suppresses further matches for this long after a
	// launch (storm damping; 0 = none).
	CooldownSeconds int `json:"cooldownSeconds,omitempty"`
	// Launch target: a single Run (ViewName + Actuator + Params + …) or a
	// declared Workflow (WorkflowName) — exactly one.
	ViewName string `json:"viewName,omitempty"`
	// ViewParams binds a parametrized View's {{.param.x}} placeholders
	// (ADR-0024); values may themselves reference the firing event
	// ({{.event.x}}). Empty for plain Views.
	ViewParams map[string]any `json:"viewParams,omitempty"`
	Actuator   string         `json:"actuator,omitempty"`
	// FacetWriteScope is the Facet namespaces a launched Run may write back
	// (ADR-0054): the actuator's grant ∩ this scope. Empty admits no facet write-back.
	FacetWriteScope []string       `json:"facetWriteScope,omitempty"`
	Params          map[string]any `json:"params,omitempty"`
	Slices          int            `json:"slices,omitempty"`
	CredentialRefs  []string       `json:"credentialRefs,omitempty"`
	// WorkflowName launches a declared Workflow instead of a single Run
	// (the ADR-0010 rider, valid for both kinds).
	WorkflowName string `json:"workflowName,omitempty"`
	// Principal is the service identity the fired Runs execute as (§2.5);
	// the dispatch-time `use` check applies to it exactly like an API launch.
	Principal string `json:"principal,omitempty"`
}
