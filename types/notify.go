package types

import "time"

// Notice kinds — the outbound platform signals a Subscription can route
// (ADR-0027). Notices are the outbound mirror of the inbound EmitterEvent:
// where an EmitterEvent turns an external happening into a Trigger launch, a
// Notice turns a notable *internal* happening into an outbound delivery.
const (
	NoticeRunFailed   = "run.failed"
	NoticeRunCanceled = "run.canceled"
	NoticeFindingOpen = "finding.open"
	NoticeGatePending = "gate.pending"
)

// Sink kinds — the outbound destinations. webhook (notify, ADR-0027) delivers
// Notices from the notifier; the SIEM kinds (ADR-0034) are audit-egress
// destinations the stratt-forwarder ships the audit stream to. "Sink" is one
// delivery-plane noun, NOT a core Named Kind (§2); the kind selects the driver.
// Splunk/syslog/OTel are drivers beneath a neutral seam — none is load-bearing
// in core (§1.4/§1.5, mirroring "S3-compatible, never MinIO-by-name").
const (
	SinkWebhook   = "webhook"
	SinkSplunkHEC = "splunk-hec"
	SinkSyslog    = "syslog"
	SinkOTelLogs  = "otel-logs"
)

// SIEMSinkKinds are the audit-egress Sink kinds the forwarder handles.
var SIEMSinkKinds = map[string]bool{SinkSplunkHEC: true, SinkSyslog: true, SinkOTelLogs: true}

// Sink is a CaC-declared outbound delivery endpoint (ADR-0027). It is
// delivery-plane infra, NOT a core-model Named Kind (§2) — hence the notify_
// table prefix, mirroring how the awx_ prefix kept compat identifiers out of
// the frozen vocabulary. Secret material (the webhook URL, a bearer token) is
// NEVER inline: it binds a CredentialRef, injected as files into the delivery
// pod at spawn (§2.5) — the control plane handles pointers only.
type Sink struct {
	Name string `json:"name"`
	// Kind is webhook (v1).
	Kind string `json:"kind"`
	// Principal is the identity deliveries authenticate as — it must hold the
	// `use` grant on CredentialRef (§2.5 use-without-read, §1.6 one authz
	// model). The notifier runs the same credential-use check the Run path
	// does before injecting the credential, so delivery cannot bypass the
	// credential's OwnerTeam scoping. CaC-declared (Git review authorizes the
	// impersonation, exactly like a Trigger/Baseline Principal).
	Principal string `json:"principal"`
	// CredentialRef names the k8s-secret-backed credential whose keys supply
	// the delivery url (and optional token/headers), injected as files into
	// the delivery pod. Required for webhook.
	CredentialRef string `json:"credentialRef"`
	// Config is non-secret delivery config.
	Config SinkConfig `json:"config,omitempty"`
}

// SinkConfig is the non-secret delivery configuration of a Sink. The webhook
// fields serve notify; the SIEM fields serve the audit forwarder. All are
// non-secret — the credential (HEC token, TLS material) is a CredentialRef,
// never inline (§2.5).
type SinkConfig struct {
	// Method is the HTTP method (default POST). (webhook)
	Method string `json:"method,omitempty"`
	// BodyTemplate is a Go text/template rendered over the Notice — it may
	// reference {{.kind}}, {{.subject}}, {{.at}}, and {{.payload.X}}. Empty
	// renders a default JSON body of the whole Notice. (webhook)
	BodyTemplate string `json:"bodyTemplate,omitempty"`

	// Endpoint is the SIEM destination: an https URL for splunk-hec / otel-logs,
	// or host:port for syslog. (SIEM)
	Endpoint string `json:"endpoint,omitempty"`
	// Index is the Splunk index; Source/SourceType tag the events. (splunk-hec)
	Index string `json:"index,omitempty"`
	// Facility is the syslog facility number (default 13/audit). (syslog)
	Facility int `json:"facility,omitempty"`
	// Insecure allows plain HTTP / no-TLS to a dev SIEM. Production is TLS. (SIEM)
	Insecure bool `json:"insecure,omitempty"`
}

// Subscription binds notice-kinds × a CEL predicate → a Sink (ADR-0027).
// Every matching Subscription fires — additive fan-out, never precedence
// (§2.4, the anti-GPO axiom). CaC-only, like Emitter/Trigger/Baseline.
type Subscription struct {
	Name string `json:"name"`
	// On is the set of notice kinds this Subscription listens for.
	On []string `json:"on"`
	// Match is an optional CEL predicate over the notice, bound as the CEL
	// `event` variable (event.kind, event.subject, event.payload.X). Empty
	// matches every notice of a listed kind.
	Match string `json:"match,omitempty"`
	// Sink names the delivery endpoint.
	Sink string `json:"sink"`
	// CooldownSeconds suppresses repeat deliveries for this Subscription
	// within the window (0 = none) — the same shape as a Trigger cooldown.
	CooldownSeconds int `json:"cooldownSeconds,omitempty"`
}

// Notice is one outbound platform signal (ADR-0027): a Run failed, a Finding
// opened, a Gate is pending. Transient — it is a bus event, never stored
// truth (§1.2). Emitted onto the notice stream, matched by Subscriptions,
// delivered via a Sink.
type Notice struct {
	// Kind is one of the Notice* values.
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Subject is the primary id (run id, finding id, gate id) — the §1.8
	// descent anchor back to the originating object.
	Subject string `json:"subject"`
	// Payload carries the fields a Subscription's CEL match and a Sink's body
	// template reference (status, severity, baseline, view, approvers, …).
	Payload map[string]any `json:"payload,omitempty"`
}

// NotifyDelivery is one recorded delivery attempt — the queryable status
// surface that keeps delivery failure from being silent (§1.8). It is a
// product surface (readable like Findings), not a second source of truth.
type NotifyDelivery struct {
	ID           string `json:"id"`
	NoticeKind   string `json:"noticeKind"`
	Subject      string `json:"subject"`
	Subscription string `json:"subscription"`
	Sink         string `json:"sink"`
	// Status is delivered | failed.
	Status string `json:"status"`
	// Detail is the error or non-2xx summary on failure (never secret
	// material — the url/token live only in the delivery pod).
	Detail string `json:"detail,omitempty"`
	// RunID links the delivery to its descendable Run (§1.8, ADR-0040); empty
	// for pre-0040 deliveries.
	RunID string    `json:"runId,omitempty"`
	At    time.Time `json:"at"`
}

// Delivery statuses.
const (
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)
