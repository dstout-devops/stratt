package types

import "time"

// Policy governance types (ADR-0061 / ADR-0062). Go structs are an internal
// convenience; the pinned JSON Schema in contracts/policy is the source of
// truth (§1.5). These are Contract-payload type names, not new Named Kinds
// (vocabulary-linter, ADR-0061).

// Outcome is the four-way governance decision (ADR-0061 decision 1). It is a
// CLOSED enum; REQUIRE_APPROVAL is today's human Gate generalised, ESCALATE
// routes to a higher authority. Ordered here by restrictiveness — see
// OutcomeRank / the most-restrictive-wins lattice (ADR-0061 M3 / guardrail 2).
const (
	OutcomeAllow           = "allow"
	OutcomeRequireApproval = "require_approval"
	OutcomeEscalate        = "escalate"
	OutcomeDeny            = "deny"
)

// OutcomeRank is the fixed, non-configurable most-restrictive-wins lattice
// (§2.4, ADR-0061 M3): deny > escalate > require_approval > allow. Higher rank
// wins, order-independently. There is no priority scalar and no configurable
// combinator. An unknown outcome ranks maximally (fail-closed).
func OutcomeRank(outcome string) int {
	switch outcome {
	case OutcomeAllow:
		return 0
	case OutcomeRequireApproval:
		return 1
	case OutcomeEscalate:
		return 2
	case OutcomeDeny:
		return 3
	default:
		return 3 // fail-closed: an unrecognised outcome is treated as deny
	}
}

// Closed obligation types (ADR-0061 guardrail 1: a closed enum; org authoring
// is parameterisation, never extension). A new obligation type is its own ADR.
const (
	ObligationRequireApproval = "require_approval" // params: count, from (selector)
	ObligationTTL             = "ttl"              // params: expires
	ObligationRecordEvidence  = "record_evidence"
	ObligationNotify          = "notify" // params: target
)

// Principal-ish reference carried in a decision request. Reuses the Principal
// identity model (§1.6); attr is sparse ambient attributes.
type PrincipalRef struct {
	ID    string         `json:"id"`
	Kind  string         `json:"kind,omitempty"`
	Roles []string       `json:"roles,omitempty"`
	Attr  map[string]any `json:"attr,omitempty"`
}

// TargetRef is one target the change touches. Criticality is an OPTIONAL,
// sparse, computed/Contract-demanded coordinate (ADR-0061 M4) — never a
// required universal Entity attribute; absent ⇒ fail-safe (most-critical).
type TargetRef struct {
	EntityRef   string `json:"entityRef"`
	Kind        string `json:"kind,omitempty"`
	Environment string `json:"environment,omitempty"`
	Criticality string `json:"criticality,omitempty"` // optional; absent ⇒ most-critical
}

// BlastRadius feeds the §4.3 max-delta gate. MaxCriticality is optional/sparse
// (M4); EntityCount/ServiceCount are structural facts the spine already knows.
type BlastRadius struct {
	EntityCount    int    `json:"entityCount"`
	ServiceCount   int    `json:"serviceCount"`
	MaxCriticality string `json:"maxCriticality,omitempty"` // optional; absent ⇒ most-critical
}

// ChangeContext is the one shared, typed evaluation input every control
// evaluates (ADR-0061 decision 1) — the unifier that keeps the spine
// content-blind. RiskScore is an optional, sparse, computed coordinate (M4).
type ChangeContext struct {
	Actor        PrincipalRef      `json:"actor"`
	Committers   []PrincipalRef    `json:"committers,omitempty"`
	Targets      []TargetRef       `json:"targets,omitempty"`
	BlastRadius  BlastRadius       `json:"blastRadius"`
	Environment  string            `json:"environment,omitempty"`
	ChangeClass  string            `json:"changeClass,omitempty"`
	RiskScore    *float64          `json:"riskScore,omitempty"` // optional; absent ⇒ most-restrictive
	ScheduledAt  time.Time         `json:"scheduledAt,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	HasRiskScore bool              `json:"-"` // true when RiskScore was supplied (fail-safe helper)
}

// Control is one governance predicate over the ChangeContext (ADR-0061 §4 /
// ADR-0062). A control FIRES its Outcome + Obligations when its predicate holds.
// A control is exactly one KIND (validated at load, ADR-0067): a raw CEL
// predicate (When), or one typed primitive from the closed Control library
// (TimeWindow, and — as they land — SoD/Waiver/Quorum/BreakGlass). Typed
// primitives are DATA the framework evaluates deterministically; they are not
// lowered to author-visible CEL (guardrail 1: parameterisation, not a DSL).
type Control struct {
	ID          string       `json:"id"`
	Type        string       `json:"type,omitempty"`
	When        string       `json:"when,omitempty"`
	Outcome     string       `json:"outcome"`
	Obligations []Obligation `json:"obligations,omitempty"`
	// TimeWindow is the change-freeze / maintenance-window primitive (§5 Flow-4).
	TimeWindow *TimeWindowSpec `json:"timeWindow,omitempty"`
	// SoD is the separation-of-duties primitive (ADR-0068).
	SoD *SoDSpec `json:"sod,omitempty"`
	// Waiver is a MODIFIER, not a predicate (ADR-0069): it exempts another
	// control in the same set from applying its outcome. A waiver control needs
	// no Outcome/When and is exclusive with the predicate kinds.
	Waiver *WaiverSpec `json:"waiver,omitempty"`
}

// WaiverSpec is a time-boxed, approved exemption of another control (ADR-0069):
// while ACTIVE (not expired at the decision time), it suppresses the referenced
// control's outcome — the change proceeds despite that control firing, and the
// suppression is RECORDED (a waiver-applied pass is compliance-relevant,
// ADR-0061 S1). ExpiresAt is MANDATORY (guardrail 4: Kyverno's missing expiry
// is a known footgun) — a waiver without it fails to compile. A waiver can only
// reference a control IN its ControlSet, so it can never exempt a
// framework-compiled mandatory floor (§4.3/§5, which are not ControlSet
// controls — ADR-0066).
type WaiverSpec struct {
	ControlRef    string    `json:"controlRef"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Justification string    `json:"justification"`
	ApprovedBy    string    `json:"approvedBy"`
}

// SoD distinct-from role sets (ADR-0068). v1 supports "committers".
const (
	// SoDDistinctFromCommitters requires the actor to differ from the change
	// authors — four-eyes at authoring: the requester may not also be a committer.
	SoDDistinctFromCommitters = "committers"
)

// SoDSpec is the separation-of-duties primitive (ADR-0068): the control FIRES
// its Outcome when the actor belongs to a role set it must be DISTINCT from —
// an SoD violation. v1 checks distinctness from `committers` (the change
// authors, carried in ChangeContext.Committers). With no committers recorded
// there is no dual-role conflict to detect, so the control does not fire (plain
// set-membership: actor ∈ ∅ is false). Approver-distinctness (requester ≠
// approver) is enforced at gate-decision time — a follow-up.
type SoDSpec struct {
	DistinctFrom []string `json:"distinctFrom"`
}

// TimeWindow modes (ADR-0067).
const (
	// TimeWindowDeny is a blackout: the control fires when scheduled_at is INSIDE
	// the window (a forbidden period — e.g. a holiday change freeze).
	TimeWindowDeny = "deny"
	// TimeWindowAllowOnly is a maintenance window: the control fires when
	// scheduled_at is OUTSIDE the window (changes are permitted only within it).
	TimeWindowAllowOnly = "allow-only"
)

// TimeWindowSpec is a recurring weekly time window in UTC (ADR-0067 v1: no RRULE
// dependency yet — days-of-week + an hour range). Days are lowercase 3-letter
// abbreviations (sun mon tue wed thu fri sat); empty = every day. The window is
// [StartHourUTC, EndHourUTC) on a matching day. An unset scheduled_at fails
// closed (most-restrictive, ADR-0061 M4) — a window cannot be judged without a
// time.
type TimeWindowSpec struct {
	Mode         string   `json:"mode"`
	Days         []string `json:"days,omitempty"`
	StartHourUTC int      `json:"startHourUtc"`
	EndHourUTC   int      `json:"endHourUtc"`
}

// Obligation is a binding rider on a decision (ADR-0061). Type is from the
// closed enum above; Params is the primitive's parameterisation.
type Obligation struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// Reason is one structured, non-opaque justification (§1.8). Every FIRED
// control contributes a Reason — not only the winning one (ADR-0061 S4).
type Reason struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	ControlID string `json:"controlId,omitempty"`
}

// DecisionProvenance stamps which evaluator + pinned policy produced a
// decision (§1.2/§1.5).
type DecisionProvenance struct {
	Engine        string    `json:"engine"`
	EngineVersion string    `json:"engineVersion,omitempty"`
	PolicyDigest  string    `json:"policyDigest,omitempty"`
	EvaluatedAt   time.Time `json:"evaluatedAt"`
}

// Decision is the four-way PDP result (ADR-0061 decision 1). Reasons enumerate
// ALL contributing controls; Obligations are the binding riders of the fired
// controls; Provenance stamps the evaluator.
type Decision struct {
	Outcome     string             `json:"outcome"`
	Reasons     []Reason           `json:"reasons,omitempty"`
	Obligations []Obligation       `json:"obligations,omitempty"`
	Provenance  DecisionProvenance `json:"provenance"`
}
