// Package forwarder is the stratt-forwarder: a long-lived pod that ships the
// one audit stream to a SIEM (ADR-0034). It reads audit batches from the
// platform API by a server-owned cursor, ships them through a vendor-neutral
// Driver, and reports delivery — committing the cursor only on success, so a
// dropped audit record is impossible by design (§1.8). The SIEM credential is
// injected into this pod at spawn (§2.5); the control plane holds only a
// pointer. Splunk/syslog/OTel are drivers beneath the neutral seam — none is
// load-bearing (§1.4/§1.5).
package forwarder

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// Driver ships a batch of audit events to one SIEM destination, in order. It
// returns an error if the destination did not accept the batch; the caller then
// retries and never advances the cursor.
type Driver interface {
	Ship(ctx context.Context, events []types.AuditEvent) error
	// Name is the driver kind, for logs and delivery reports.
	Name() string
}

// SinkConfig is the resolved egress config: the declared Sink's non-secret
// fields (from the API) plus the credential material read from the injected
// mount (§2.5) — never sourced from the control plane.
type SinkConfig struct {
	Kind     string
	Endpoint string
	Index    string
	Facility int
	Insecure bool
	// Token is the SIEM credential (Splunk HEC token / OTel bearer), read from
	// the injected file. Empty for an unauthenticated dev endpoint.
	Token string
	// TLS, when set, secures a syslog TCP connection.
	TLS *tls.Config
}

// NewDriver builds the driver for a sink kind.
func NewDriver(c SinkConfig) (Driver, error) {
	switch c.Kind {
	case types.SinkSplunkHEC:
		return &splunkDriver{cfg: c, client: httpClient(c)}, nil
	case types.SinkSyslog:
		return &syslogDriver{cfg: c}, nil
	case types.SinkOTelLogs:
		return &otelDriver{cfg: c, client: httpClient(c)}, nil
	default:
		return nil, fmt.Errorf("forwarder: unknown sink kind %q (splunk-hec, syslog, otel-logs)", c.Kind)
	}
}

func httpClient(c SinkConfig) *http.Client {
	tr := &http.Transport{}
	if c.TLS != nil {
		tr.TLSClientConfig = c.TLS
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// eventJSON flattens an audit event for a SIEM payload — the same field set
// across drivers so a rule written for one destination reads the same on
// another. Never includes secret material (§2.5).
func eventJSON(e types.AuditEvent) map[string]any {
	m := map[string]any{
		"seq":    e.Seq,
		"at":     e.At.UTC().Format(time.RFC3339Nano),
		"action": e.Action,
	}
	if e.PrincipalID != "" {
		m["principal"] = e.PrincipalID
	}
	if e.PrincipalKind != "" {
		m["principalKind"] = e.PrincipalKind
	}
	if e.Object != "" {
		m["object"] = e.Object
	}
	if e.Outcome != "" {
		m["outcome"] = e.Outcome
	}
	if len(e.Detail) > 0 {
		var d any
		if json.Unmarshal(e.Detail, &d) == nil {
			m["detail"] = d
		}
	}
	return m
}
