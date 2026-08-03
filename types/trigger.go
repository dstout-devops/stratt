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

	// ── deciding on more than one event (ADR-0162) ────────────────────────────────────────────
	//
	// `When` still answers exactly one question about exactly one event. These say what PATTERN of
	// matching events fires the Trigger — data, not a language (§1), the same shape
	// CooldownSeconds already is.

	// WithinSeconds is the window the pattern below is measured over. Required with Count > 1 or
	// AllOf; meaningless alone.
	WithinSeconds int `json:"withinSeconds,omitempty"`
	// Count fires the Trigger when this many MATCHING events land inside the window, and the window
	// then RESETS. 0 or 1 ⇒ fire on every match, which is every Trigger that exists today.
	//
	// Reset rather than slide, deliberately: a sliding window fires on the 5th event and then the
	// 6th and the 7th, so a storm produces a storm of Runs — the problem this exists to solve.
	Count int `json:"count,omitempty"`
	// AllOf fires the Trigger when EVERY one of these conditions has been satisfied by some event
	// sharing one CorrelateBy value, inside the window. Mutually exclusive with When and with Count:
	// "five of these" and "one of each of those" are different questions, and a Trigger declaring
	// both would need a rule to combine them (§2.4).
	AllOf []string `json:"allOf,omitempty"`
	// CorrelateBy is the dotted PATH into the event payload whose value ties events together —
	// REQUIRED with AllOf. Without it "a deploy finished somewhere and a health check failed
	// somewhere" fires, which is not what anybody means and is a very good way to converge the wrong
	// estate at 3am. AAP's all() has this hazard and leaves it to the author; here the mistake is
	// unavailable.
	//
	// A PATH, not an expression (ADR-0024): explicit field lookup, nothing evaluated. A value whose
	// only job is to equal itself does not need a second evaluation surface.
	CorrelateBy string `json:"correlateBy,omitempty"`
	// Launch target: a single Run (ViewName + Actuator + Params + …) or a
	// declared Workflow (WorkflowName) — exactly one.
	ViewName string `json:"viewName,omitempty"`
	// ViewParams binds a parametrized View's {{.param.x}} placeholders
	// (ADR-0024); values may themselves reference the firing event
	// ({{.event.x}}). Empty for plain Views.
	ViewParams map[string]any `json:"viewParams,omitempty"`
	Actuator   string         `json:"actuator,omitempty"`
	// ActuatorCapability names a capability CLASS instead of an Actuator (ADR-0140 D4):
	// the declaration says WHAT must converge and the bound provider is resolved at
	// launch, so a provider swap edits no Trigger. Mutually exclusive with Actuator.
	//
	// FacetWriteScope below is the declaration's half of the write ceiling (ADR-0054,
	// grant â© scope) and the grant belongs to the RESOLVED Actuator. So a
	// capability-named actuation is checked at load against EVERY candidate provider's
	// grant, not the one bound today: a scope that fits one provider and exceeds another
	// is a write that silently stops happening on a rebind, and a dropped write-back
	// reports as nothing at all rather than as an error.
	ActuatorCapability string `json:"actuatorCapability,omitempty"`
	// FacetWriteScope is the Facet namespaces a launched Run may write back
	// (ADR-0054): the actuator's grant ∩ this scope. Empty admits no facet write-back.
	FacetWriteScope []string       `json:"facetWriteScope,omitempty"`
	Params          map[string]any `json:"params,omitempty"`
	Slices          int            `json:"slices,omitempty"`
	CredentialRefs  []string       `json:"credentialRefs,omitempty"`
	// Inputs are the LAUNCH INPUTS for a WorkflowName target — the values its declared
	// `inputs` schema validates, bound in Step params via {{.launch.x}} (ADR-0118 D5).
	//
	// A SEPARATE field from Params on purpose. Params are Step fields (Actuator params), and a
	// Workflow-target Trigger is correctly forbidden from carrying those: "the Workflow declares
	// its own". Reusing Params for launch inputs would make one field mean two different things
	// depending on the target — the exact overloading that made LaunchParams carry both a
	// Workflow's parameters and the policy change context (ADR-0118 D4a), and that had to be
	// undone to type either of them.
	//
	// Valid ONLY on a Workflow target; a Run target has no launch interface to fill. For an
	// EVENT Trigger these may bind {{.event.x}} and are validated after substitution at launch;
	// for a SCHEDULE Trigger they are literal and validated at DECLARATION, where the mistake is
	// still cheap.
	Inputs map[string]any `json:"inputs,omitempty"`
	// WorkflowName launches a declared Workflow instead of a single Run
	// (the ADR-0010 rider, valid for both kinds).
	WorkflowName string `json:"workflowName,omitempty"`
	// Principal is the service identity the fired Runs execute as (§2.5);
	// the dispatch-time `use` check applies to it exactly like an API launch.
	Principal string `json:"principal,omitempty"`
	// Environments scopes this Trigger to a subset of environments (ADR-0057);
	// empty = all. A scoped daemon (STRATT_ENVIRONMENT) reconciles it only when
	// in scope, so a dev cell never fires prod's schedules.
	Environments []string `json:"environments,omitempty"`
}
