package forwarder

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// Config is the forwarder's runtime configuration. The non-secret egress config
// (kind, endpoint, …) is fetched from the platform (the declared Sink is the
// source of truth); the credential is read from CredentialDir, injected into
// this pod at spawn (§2.5).
type Config struct {
	Server        string // platform API base, e.g. http://strattd:8080
	Sink          string // the SIEM Sink name (offset key)
	AuthHeader    string // "Authorization" or "X-Stratt-Principal"
	AuthValue     string // the bearer token or principal id
	PrincipalKind string // for the dev header path
	CredentialDir string // injected SIEM credential mount
	BatchLimit    int
	Interval      time.Duration // poll interval when caught up / backoff base
	FailAlarm     int           // consecutive failures before recording a failed delivery
	Log           *slog.Logger
}

// Run fetches the sink config, builds the driver, then ships the audit stream
// in order until ctx is cancelled. The cursor lives on the server; this loop
// advances it only by reporting a delivery — so a crash or a down SIEM never
// loses an event (ADR-0034, §1.8).
func (c Config) Run(ctx context.Context) error {
	if c.BatchLimit <= 0 {
		c.BatchLimit = 200
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.FailAlarm <= 0 {
		c.FailAlarm = 3
	}
	client := &http.Client{Timeout: 30 * time.Second}

	cfg, err := c.fetchConfig(ctx, client)
	if err != nil {
		return err
	}
	if err := c.loadCredential(&cfg); err != nil {
		return err
	}
	driver, err := NewDriver(cfg)
	if err != nil {
		return err
	}
	c.Log.Info("forwarder ready", "sink", c.Sink, "driver", driver.Name(), "endpoint", cfg.Endpoint)

	fails := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		events, err := c.getBatch(ctx, client)
		if err != nil {
			c.Log.Error("forwarder: get batch", "err", err)
			c.sleep(ctx, c.Interval)
			continue
		}
		if len(events) == 0 {
			c.sleep(ctx, c.Interval)
			continue
		}
		through := events[len(events)-1].Seq
		if err := driver.Ship(ctx, events); err != nil {
			fails++
			c.Log.Error("forwarder: ship failed — will retry, offset NOT advanced", "err", err, "through", through, "fails", fails)
			if fails >= c.FailAlarm {
				// Record the persistent failure for visibility (§1.8); never
				// advance — the batch re-ships until it lands.
				c.report(ctx, client, forwardReport{ThroughSeq: through, Count: len(events), Status: types.ForwardFailed, Detail: err.Error()})
			}
			c.sleep(ctx, backoff(c.Interval, fails))
			continue
		}
		fails = 0
		if err := c.report(ctx, client, forwardReport{ThroughSeq: through, Count: len(events), Status: types.ForwardDelivered}); err != nil {
			// Delivered to the SIEM but the offset commit failed: the batch
			// re-ships (at-least-once — the SIEM dedups on seq). Never drop.
			c.Log.Error("forwarder: commit failed after ship — batch will re-ship", "err", err, "through", through)
			c.sleep(ctx, c.Interval)
			continue
		}
		c.Log.Info("forwarder: shipped", "count", len(events), "through", through)
	}
}

func (c Config) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func backoff(base time.Duration, fails int) time.Duration {
	d := base
	for i := 1; i < fails && d < 2*time.Minute; i++ {
		d *= 2
	}
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}

// ── platform API client ──────────────────────────────────────────────────

func (c Config) req(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Server+"/api/v1"+path, body)
	if err != nil {
		return nil, err
	}
	if c.AuthHeader != "" && c.AuthValue != "" {
		req.Header.Set(c.AuthHeader, c.AuthValue)
		if c.AuthHeader == "X-Stratt-Principal" && c.PrincipalKind != "" {
			req.Header.Set("X-Stratt-Principal-Kind", c.PrincipalKind)
		}
	}
	return req, nil
}

func (c Config) fetchConfig(ctx context.Context, client *http.Client) (SinkConfig, error) {
	req, err := c.req(ctx, http.MethodGet, "/audit/forward/"+c.Sink+"/config", nil)
	if err != nil {
		return SinkConfig{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return SinkConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return SinkConfig{}, fmt.Errorf("forwarder: sink config %s: %s: %s", c.Sink, resp.Status, b)
	}
	var fc struct {
		Kind, Endpoint, Index string
		Facility              int
		Insecure              bool
	}
	if err := json.NewDecoder(resp.Body).Decode(&fc); err != nil {
		return SinkConfig{}, err
	}
	return SinkConfig{Kind: fc.Kind, Endpoint: fc.Endpoint, Index: fc.Index, Facility: fc.Facility, Insecure: fc.Insecure}, nil
}

func (c Config) getBatch(ctx context.Context, client *http.Client) ([]types.AuditEvent, error) {
	req, err := c.req(ctx, http.MethodGet, fmt.Sprintf("/audit/forward/%s?limit=%d", c.Sink, c.BatchLimit), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, b)
	}
	var evs []types.AuditEvent
	if err := json.NewDecoder(resp.Body).Decode(&evs); err != nil {
		return nil, err
	}
	return evs, nil
}

type forwardReport struct {
	ThroughSeq int64  `json:"throughSeq"`
	Count      int    `json:"count"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
}

func (c Config) report(ctx context.Context, client *http.Client, rep forwardReport) error {
	raw, _ := json.Marshal(rep)
	req, err := c.req(ctx, http.MethodPost, "/audit/forward/"+c.Sink+"/report", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("report %s: %s", resp.Status, b)
	}
	return nil
}

// loadCredential reads the SIEM credential from the injected mount (§2.5): a
// `token` file (Splunk HEC / OTel bearer) and optional TLS material for syslog.
// Missing files are fine — a dev endpoint may be unauthenticated/plain.
func (c Config) loadCredential(cfg *SinkConfig) error {
	if c.CredentialDir == "" {
		return nil
	}
	if b, err := os.ReadFile(filepath.Join(c.CredentialDir, "token")); err == nil {
		cfg.Token = string(bytes.TrimSpace(b))
	}
	if cfg.Kind == types.SinkSyslog {
		if tlsCfg := c.loadTLS(); tlsCfg != nil {
			cfg.TLS = tlsCfg
		}
	}
	return nil
}

func (c Config) loadTLS() *tls.Config {
	ca, err := os.ReadFile(filepath.Join(c.CredentialDir, "ca.crt"))
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}
