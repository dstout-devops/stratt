package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// splunkDriver ships to a Splunk HTTP Event Collector (ADR-0034). One POST per
// batch to <endpoint>/services/collector/event, authenticated with the HEC
// token (`Authorization: Splunk <token>`, injected §2.5). The body is one HEC
// event object per line — HEC's batch format.
type splunkDriver struct {
	cfg    SinkConfig
	client *http.Client
}

func (d *splunkDriver) Name() string { return types.SinkSplunkHEC }

func (d *splunkDriver) Ship(ctx context.Context, events []types.AuditEvent) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	for _, e := range events {
		ev := map[string]any{
			"time":       float64(e.At.UnixNano()) / 1e9,
			"source":     "stratt",
			"sourcetype": "stratt:audit",
			"event":      eventJSON(e),
		}
		if d.cfg.Index != "" {
			ev["index"] = d.cfg.Index
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	url := strings.TrimRight(d.cfg.Endpoint, "/") + "/services/collector/event"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.cfg.Token != "" {
		req.Header.Set("Authorization", "Splunk "+d.cfg.Token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("splunk-hec: post failed") // sanitized: no url/token (§2.5)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("splunk-hec: rejected batch (status %d)", resp.StatusCode)
	}
	return nil
}
