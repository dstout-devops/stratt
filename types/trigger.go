package types

// Trigger kinds. v1 ships schedule only; the Phase-2 Trigger engine adds
// event-driven kinds (Emitter event × CEL) to this same object.
const (
	TriggerSchedule = "schedule"
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
	// Cron is the schedule spec (standard cron syntax; Temporal validates).
	Cron string `json:"cron"`
	// Paused declares the schedule paused without deleting it (drills,
	// maintenance windows).
	Paused bool `json:"paused,omitempty"`
	// ViewName, Actuator, Params, Slices, CredentialRefs mirror StartRun —
	// the launch parameters every fired Run starts with.
	ViewName       string         `json:"viewName"`
	Actuator       string         `json:"actuator,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	Slices         int            `json:"slices,omitempty"`
	CredentialRefs []string       `json:"credentialRefs,omitempty"`
	// Principal is the service identity the fired Runs execute as (§2.5);
	// the dispatch-time `use` check applies to it exactly like an API launch.
	Principal string `json:"principal,omitempty"`
}
