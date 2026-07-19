package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PromConfig configures the Prometheus-flavor TrafficSource. The mesh specifics live
// ENTIRELY here as data (the "nothing is baked in" line, §1.4): the PromQL query and
// the two result labels that carry the caller/callee FQDN. The defaults target a
// standard Istio telemetry setup, but a Linkerd/Consul/Cilium deployment is reached by
// changing this config, never this code.
type PromConfig struct {
	Endpoint  string        // Prometheus base URL, e.g. http://prometheus.istio-system:9090
	Query     string        // PromQL returning a vector whose series carry FromLabel/ToLabel
	FromLabel string        // result label holding the caller FQDN (default "source_fqdn")
	ToLabel   string        // result label holding the callee FQDN (default "destination_fqdn")
	Timeout   time.Duration // per-query timeout (default 30s)
}

// DefaultQuery is a reasonable Istio instant query: every source→destination service
// pair with request traffic in the window, projected to caller/callee FQDN labels via
// label_replace so the transport stays mesh-agnostic (it only reads FromLabel/ToLabel).
// Operators override Query for their mesh's metric + label schema.
const DefaultQuery = `label_replace(label_replace(` +
	`sum by (source_canonical_service, source_workload_namespace, destination_service_name, destination_service_namespace) ` +
	`(rate(istio_requests_total{reporter="destination"}[5m])) > 0` +
	`, "source_fqdn", "$1.$2.svc.cluster.local", "source_canonical_service", "(.+)")` +
	`, "destination_fqdn", "$1.$2.svc.cluster.local", "destination_service_name", "(.+)")`

// PrometheusSource reads mesh request telemetry from a Prometheus HTTP API. It uses the
// stdlib only (no metrics-client dependency) — the query API is a single GET returning
// a JSON result vector, so a heavy client would be pure surface. It maps each series'
// FromLabel/ToLabel onto a TrafficEdge; the abstract Normalize does the rest.
type PrometheusSource struct {
	cfg    PromConfig
	client *http.Client
}

// NewPrometheusSource builds the transport, applying defaults for the query, labels,
// and timeout. client may be nil (a default client with the configured timeout is used).
func NewPrometheusSource(cfg PromConfig, client *http.Client) *PrometheusSource {
	if cfg.Query == "" {
		cfg.Query = DefaultQuery
	}
	if cfg.FromLabel == "" {
		cfg.FromLabel = "source_fqdn"
	}
	if cfg.ToLabel == "" {
		cfg.ToLabel = "destination_fqdn"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &PrometheusSource{cfg: cfg, client: client}
}

// promResponse is the shape of a Prometheus /api/v1/query instant-vector reply.
type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
		} `json:"result"`
	} `json:"data"`
}

// Edges runs the configured instant query and maps each result series onto a
// TrafficEdge by reading the caller/callee FQDN labels. A series missing either label
// is skipped (an incomplete edge, never a guess); the abstract Normalize drops any
// remaining self-edges and dedups.
func (p *PrometheusSource) Edges(ctx context.Context) ([]TrafficEdge, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	q := url.Values{"query": {p.cfg.Query}}
	u := p.cfg.Endpoint + "/api/v1/query?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("mesh: build query request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mesh: query prometheus: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mesh: prometheus status %d", resp.StatusCode)
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("mesh: decode prometheus response: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("mesh: prometheus query failed: %s", pr.Error)
	}

	edges := make([]TrafficEdge, 0, len(pr.Data.Result))
	for _, series := range pr.Data.Result {
		from := series.Metric[p.cfg.FromLabel]
		to := series.Metric[p.cfg.ToLabel]
		if from == "" || to == "" {
			continue // incomplete series — skip, never guess an endpoint
		}
		edges = append(edges, TrafficEdge{FromFQDN: from, ToFQDN: to})
	}
	return edges, nil
}
