// Package observability wires the OpenTelemetry providers for the control plane
// (enterprise-readiness OBS-1). Before this, the daemon emitted only slog lines to
// stderr — the charter's SLOs (§7) were unmeasurable from a running system, and
// there was no `/metrics`, no traces, no way to see p95 latency or error rate.
//
// Design:
//   - Metrics ALWAYS export to a Prometheus registry backing `/metrics`, so an
//     operator gets SLO signals with zero collector — the most portable surface.
//   - Traces and a second (push) metric stream export via OTLP/gRPC ONLY when
//     OTEL_EXPORTER_OTLP_ENDPOINT is set; otherwise the tracer is a no-op. No
//     endpoint ⇒ no background exporter, no error — observability degrades to
//     `/metrics` only, never to a boot failure (§1.8: never hide, never break).
//
// The global otel providers are set here so any package (orchestrate, pluginhost,
// the API) instruments through `otel.Meter(...)` / `otel.Tracer(...)` without
// threading a handle. Shutdown flushes the batchers.
package observability

import (
	"context"
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

// Config is the daemon's telemetry identity + export target.
type Config struct {
	ServiceName    string // "stratt"
	ServiceVersion string // build version; "" ⇒ omitted
	Cell           string // STRATT_CELL_ID, stamped on every signal for multi-region
	// OTLPEndpoint is OTEL_EXPORTER_OTLP_ENDPOINT. Empty ⇒ no OTLP push (traces are
	// a no-op; metrics still serve on /metrics). The OTLP exporters also read the
	// standard OTEL_EXPORTER_OTLP_* env themselves; this flag only gates whether we
	// construct them at all.
	OTLPEndpoint string
}

// Providers holds the configured telemetry surface and its shutdown hooks.
type Providers struct {
	registry       *prometheus.Registry
	metricsHandler http.Handler
	shutdowns      []func(context.Context) error
}

// Setup constructs the providers, sets them as the otel globals, and returns the
// handle. It never fails on a missing OTLP endpoint — only on a genuinely broken
// exporter/registry construction.
func Setup(ctx context.Context, cfg Config) (*Providers, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Cell != "" {
		attrs = append(attrs, attribute.String("stratt.cell", cfg.Cell))
	}
	res, err := resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, attrs...))
	if err != nil {
		// A schema-URL mismatch between Default() and ours is non-fatal: fall back
		// to just our attributes rather than refusing to boot observability.
		res = resource.NewWithAttributes(semconv.SchemaURL, attrs...)
	}

	p := &Providers{}

	// Metrics: a dedicated Prometheus registry (not the global default, so tests
	// and multiple instances don't collide) backing /metrics — always on.
	reg := prometheus.NewRegistry()
	promExp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, err
	}
	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(res), sdkmetric.WithReader(promExp)}

	if cfg.OTLPEndpoint != "" {
		mexp, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)))
	}
	mp := sdkmetric.NewMeterProvider(metricOpts...)
	otel.SetMeterProvider(mp)
	p.shutdowns = append(p.shutdowns, mp.Shutdown)
	p.registry = reg
	p.metricsHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	// Traces: OTLP only. No endpoint ⇒ a no-op provider (zero overhead, still lets
	// instrumented code call otel.Tracer without nil checks).
	var tp trace.TracerProvider = tracenoop.NewTracerProvider()
	if cfg.OTLPEndpoint != "" {
		texp, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		stp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(texp))
		tp = stp
		p.shutdowns = append(p.shutdowns, stp.Shutdown)
	}
	otel.SetTracerProvider(tp)

	// W3C tracecontext + baggage so a trace spans the API → plugin gRPC boundary.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	return p, nil
}

// MetricsHandler serves the Prometheus exposition for the daemon's own registry.
// Mount it (unauthenticated, scrape-only) at /metrics.
func (p *Providers) MetricsHandler() http.Handler {
	if p == nil || p.metricsHandler == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	}
	return p.metricsHandler
}

// Shutdown flushes and stops every exporter. Safe to call on a nil/zero Providers.
func (p *Providers) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	var errs []error
	for _, fn := range p.shutdowns {
		if err := fn(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
