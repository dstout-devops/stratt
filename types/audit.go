package types

import (
	"encoding/json"
	"time"
)

// AuditEvent is one entry in the audit stream (charter §1.6): a born-here,
// Principal-stamped record of one action — who did what, to what, with what
// outcome, when. Ordered by Seq (a monotonic sequence), hash-chained for
// tamper-evidence (ADR-0034). Distinct from Provenance (§2.1): Provenance
// stamps a graph attribute WRITE; an AuditEvent stamps an ACTION.
type AuditEvent struct {
	// Seq is the monotonic order, assigned by the store on append (0 before).
	Seq int64     `json:"seq"`
	At  time.Time `json:"at"`
	// PrincipalID / PrincipalKind are the acting identity — empty for an
	// anonymous/unauthenticated request (still recorded: an attempt is audit).
	PrincipalID   string `json:"principalId"`
	PrincipalKind string `json:"principalKind,omitempty"`
	// Action names what was done (e.g. "GET /findings", "run.start",
	// "authz.exec-grant", "gate.decision", "mcp.tool-call").
	Action string `json:"action"`
	// Object is the target the action touched (a View, Run id, path, credential
	// ref, tool, …); empty when the action has no single object.
	Object string `json:"object,omitempty"`
	// Outcome is the result: "ok" | "denied" | "failed" | an HTTP status, etc.
	Outcome string `json:"outcome,omitempty"`
	// Detail is optional structured context (never secret material — §2.5).
	Detail json.RawMessage `json:"detail,omitempty"`
	// PrevHash / Hash are the tamper-evidence chain, set by the sealer; nil on
	// the unsealed tail.
	PrevHash []byte `json:"prevHash,omitempty"`
	Hash     []byte `json:"hash,omitempty"`
}

// Audit action constants — the stable Action vocabulary (one audit path, §1.6).
const (
	AuditRunStart      = "run.start"
	AuditRunCancel     = "run.cancel"
	AuditRunFinish     = "run.finish"
	AuditDesiredApply  = "desired-state.apply"
	AuditGateDecision  = "gate.decision"
	AuditExecGrant     = "authz.exec-grant"
	AuditCredentialUse = "credential.use"
	AuditMCPToolCall   = "mcp.tool-call"
)

// Audit outcome constants.
const (
	AuditOK     = "ok"
	AuditDenied = "denied"
	AuditFailed = "failed"
)

// AuditVerification is the result of walking the tamper-evidence hash chain
// (ADR-0034): OK when every sealed event's stored hash matches the recomputed
// chain and the sealed prefix reaches the seal head. On a break it names the
// first offending seq and why (altered content, broken link, or a missing
// tail) — never hiding the failure (§1.8).
type AuditVerification struct {
	OK            bool   `json:"ok"`
	SealedThrough int64  `json:"sealedThrough"`
	Events        int64  `json:"events"`
	FirstBadSeq   int64  `json:"firstBadSeq,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// ForwardDelivery is one SIEM-egress outcome (ADR-0034, §1.8): a batch shipped
// to a Sink up through ThroughSeq, its status, and a non-secret detail. It
// makes the forwarder itself observable; it never carries event bodies or
// credential material.
type ForwardDelivery struct {
	Sink       string    `json:"sink"`
	ThroughSeq int64     `json:"throughSeq"`
	Count      int       `json:"count"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	At         time.Time `json:"at"`
}

// Forward delivery statuses.
const (
	ForwardDelivered = "delivered"
	ForwardFailed    = "failed"
)
