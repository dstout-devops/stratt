package types

// Finding severities, declared on the Baseline and stamped onto every
// Finding it raises.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Baseline is desired state made checkable (charter §2.4): a View selector +
// a check Step + an optional remediation Workflow ref + a cadence. v1 ships
// the hand-written rung of the §6 ladder — the Intent/Blueprint compiler
// (Phase 2, separate slice) emits the compiled kind into the same object.
//
// A Baseline's check must never mutate the estate: the platform enforces
// read-only execution per Actuator (opentofu mode=plan; ansible check mode),
// not by convention. Baselines are CaC-only; Principal names the service
// identity the checks execute as — the ADR-0010 impersonation-via-Git
// posture, unchanged.
type Baseline struct {
	Name string `json:"name"`
	// Check Step: which View to check, with which Actuator and params.
	ViewName       string         `json:"viewName"`
	Actuator       string         `json:"actuator"`
	Params         map[string]any `json:"params,omitempty"`
	Slices         int            `json:"slices,omitempty"`
	CredentialRefs []string       `json:"credentialRefs,omitempty"`
	Principal      string         `json:"principal,omitempty"`
	// Cron is the check cadence (a Temporal Schedule actuates it, §3:
	// "Baseline cadences").
	Cron string `json:"cron"`
	// Paused declares the cadence paused without deleting it.
	Paused bool `json:"paused,omitempty"`
	// Severity stamps the Findings this Baseline raises (info|warning|
	// critical).
	Severity string `json:"severity"`
	// DampingObservations is the flap-damping N (charter §4.3): a Finding
	// opens only after N consecutive drifted observations. 0 means 1.
	DampingObservations int `json:"dampingObservations,omitempty"`
	// RemediationWorkflow names the declared Workflow that remediates this
	// Baseline's Findings. A ref only — remediation is never auto-launched
	// (§5 Flow 2: remediation behind Gates).
	RemediationWorkflow string `json:"remediationWorkflow,omitempty"`
	// Framework tags the Findings (e.g. "cis") — "one kind, framework-
	// tagged" (§2.4).
	Framework string `json:"framework,omitempty"`
}
