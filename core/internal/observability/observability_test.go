package observability

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
)

// TestSetup_NoEndpoint proves the degrade-not-break contract (OBS-1): with no
// OTLP endpoint, Setup succeeds, traces are a no-op, and /metrics still serves.
func TestSetup_NoEndpoint(t *testing.T) {
	ctx := context.Background()
	obs, err := Setup(ctx, Config{ServiceName: "stratt", ServiceVersion: "test", Cell: "local"})
	if err != nil {
		t.Fatalf("Setup with no OTLP endpoint must succeed: %v", err)
	}
	t.Cleanup(func() { _ = obs.Shutdown(context.Background()) })

	// Record a metric through the global provider that Setup installed, then scrape.
	m := otel.Meter("test")
	ctr, err := m.Int64Counter("stratt_test_events_total")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	ctr.Add(ctx, 3)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	obs.MetricsHandler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "stratt_test_events_total") {
		t.Fatalf("/metrics exposition missing the recorded counter; got:\n%s", body)
	}
}

// TestMetricsHandler_NilSafe proves a zero Providers doesn't panic and 503s.
func TestMetricsHandler_NilSafe(t *testing.T) {
	var p *Providers
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	p.MetricsHandler().ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("nil providers /metrics = %d, want 503", rec.Code)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
}
