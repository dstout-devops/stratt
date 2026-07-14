package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// otelDriver ships OTLP/HTTP logs as JSON (ADR-0034): one POST per batch to
// <endpoint>/v1/logs with a minimal ExportLogsServiceRequest, so audit rides
// the same OTel pipeline as platform telemetry. Hand-rolled JSON keeps the
// forwarder a dependency-free static binary (no OTel SDK). An optional bearer
// token (injected §2.5) authenticates to the collector.
type otelDriver struct {
	cfg    SinkConfig
	client *http.Client
}

func (d *otelDriver) Name() string { return types.SinkOTelLogs }

func (d *otelDriver) Ship(ctx context.Context, events []types.AuditEvent) error {
	records := make([]map[string]any, 0, len(events))
	for _, e := range events {
		body, _ := json.Marshal(eventJSON(e))
		records = append(records, map[string]any{
			"timeUnixNano": strconv.FormatInt(e.At.UnixNano(), 10),
			"severityText": sevText(e),
			"body":         map[string]any{"stringValue": string(body)},
			"attributes":   otelAttrs(e),
		})
	}
	payload := map[string]any{
		"resourceLogs": []map[string]any{{
			"resource": map[string]any{"attributes": []map[string]any{
				attr("service.name", "stratt"),
			}},
			"scopeLogs": []map[string]any{{
				"scope":      map[string]any{"name": "stratt.audit"},
				"logRecords": records,
			}},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(d.cfg.Endpoint, "/") + "/v1/logs"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("otel-logs: post failed") // sanitized (§2.5)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("otel-logs: rejected batch (status %d)", resp.StatusCode)
	}
	return nil
}

func otelAttrs(e types.AuditEvent) []map[string]any {
	out := []map[string]any{attr("action", e.Action)}
	if e.PrincipalID != "" {
		out = append(out, attr("principal", e.PrincipalID))
	}
	if e.Object != "" {
		out = append(out, attr("object", e.Object))
	}
	if e.Outcome != "" {
		out = append(out, attr("outcome", e.Outcome))
	}
	return out
}

func attr(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

func sevText(e types.AuditEvent) string {
	switch e.Outcome {
	case types.AuditDenied, types.AuditFailed:
		return "WARN"
	default:
		return "INFO"
	}
}
