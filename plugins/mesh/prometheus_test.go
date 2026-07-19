package mesh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestPrometheusSource_Edges: the transport GETs /api/v1/query and maps each series'
// configured caller/callee labels onto TrafficEdges, skipping series missing either
// label (never guessing an endpoint). Fixture-tested against a canned Prometheus reply.
func TestPrometheusSource_Edges(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"vector","result":[
				{"metric":{"src":"web.prod.svc.cluster.local","dst":"api.prod.svc.cluster.local"},"value":[0,"7"]},
				{"metric":{"src":"api.prod.svc.cluster.local","dst":"db.prod.svc.cluster.local"},"value":[0,"3"]},
				{"metric":{"src":"orphan.prod.svc.cluster.local"},"value":[0,"1"]}
			]}
		}`))
	}))
	defer srv.Close()

	src := NewPrometheusSource(PromConfig{
		Endpoint:  srv.URL,
		Query:     "my-mesh-query",
		FromLabel: "src",
		ToLabel:   "dst",
	}, srv.Client())

	edges, err := src.Edges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "my-mesh-query" {
		t.Fatalf("query not forwarded, got %q", gotQuery)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges (the label-less series skipped), got %d: %v", len(edges), edges)
	}
	if edges[0].FromFQDN != "web.prod.svc.cluster.local" || edges[0].ToFQDN != "api.prod.svc.cluster.local" {
		t.Fatalf("edge[0] mismapped: %+v", edges[0])
	}

	// End-to-end: the transport output normalizes into anchors + depends-on edges.
	ents := Normalize(edges)
	if len(ents) != 3 { // web, api, db
		t.Fatalf("expected 3 anchors from the 2 edges, got %d", len(ents))
	}
}

// TestPrometheusSource_QueryError: a Prometheus error status is surfaced, never
// silently treated as "no dependencies" (which would wrongly collect every edge).
func TestPrometheusSource_QueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()

	src := NewPrometheusSource(PromConfig{Endpoint: srv.URL}, srv.Client())
	if _, err := src.Edges(context.Background()); err == nil {
		t.Fatal("a Prometheus error status must surface as an error, not an empty (all-collect) snapshot")
	}
}

// TestPrometheusSource_Defaults: an unset query/labels fall back to the Istio defaults.
func TestPrometheusSource_Defaults(t *testing.T) {
	src := NewPrometheusSource(PromConfig{Endpoint: "http://x"}, nil)
	if src.cfg.Query != DefaultQuery {
		t.Fatal("query must default to the Istio DefaultQuery")
	}
	if src.cfg.FromLabel != "source_fqdn" || src.cfg.ToLabel != "destination_fqdn" {
		t.Fatalf("labels must default: %q/%q", src.cfg.FromLabel, src.cfg.ToLabel)
	}
	// The default query is a well-formed value we hand to Prometheus verbatim.
	if _, err := url.Parse(src.cfg.Endpoint); err != nil {
		t.Fatal(err)
	}
}
