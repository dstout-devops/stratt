# ADR 0077 — Observability: OpenTelemetry providers, `/metrics` always-on, OTLP optional

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Charter sections:** §3 (substrate: OTel named), §1.4 (boring spine, huge community), §1.7 (evergreen), §1.8 (never hide diagnosis), §7 (SLOs)
- **Implements:** the enterprise-readiness crack fix **OBS-1**
- **Dependency review:** dependency-scout — RECOMMEND (see below)

## Context

The daemon emitted only slog text to stderr. There was no `TracerProvider`, no metrics, no `/metrics`, no `/metrics` Helm wiring — OTel was an *indirect* dependency pulled in transitively and never initialised. The charter's SLOs (§7: pod-spawn p95, view freshness, availability) were **unmeasurable from a running system**, and an operator had no latency/error-rate/throughput signal at all. §1.8 ("the abstraction must never hide diagnosis") and §7 both require the opposite. This is the observability half of the operational envelope an enterprise bets on.

## Decision

**Wire the OpenTelemetry Go SDK as a first-class provider layer, with a portable-by-default metrics surface.**

**1. Metrics always export to a Prometheus registry backing `/metrics`.** An operator gets SLO signals with **zero collector** — the most portable surface, scrapeable by any Prometheus. Served unauthenticated (the scrape convention), network-scoped by the SEC-3 NetworkPolicy rather than by a Principal.

**2. Traces + a second (push) metric stream export via OTLP/gRPC ONLY when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.** No endpoint ⇒ the tracer is a no-op provider (zero overhead, no nil-checks at call sites) and no background exporter runs. Observability degrades to `/metrics`-only, **never to a boot failure** (§1.8): a missing endpoint is not an error.

**3. The global otel providers are set at boot**, so any package instruments through `otel.Meter(...)` / `otel.Tracer(...)` without threading a handle. Every signal is stamped with `service.name=stratt`, the build version, and the **Cell id** (multi-region attributability).

**4. The API surface is wrapped with `otelhttp`** — every `/api/v1` request produces a server span + `http.server.*` metrics (duration, count, in-flight): the raw latency-p95 / error-rate / throughput SLO signals, for free, across the whole API.

**5. Helm wires it:** `observability.otlpEndpoint` → `OTEL_EXPORTER_OTLP_ENDPOINT` (+ `otlpEnv` passthrough), and an opt-in Prometheus-Operator `PodMonitor` scraping `:8080/metrics`.

### Dependency verdict (§1.7)

dependency-scout returned **RECOMMEND**. Key findings: the metrics SDK is **GA and lockstepped** with traces (both `v1.44.0` — not churning, contrary to its historical reputation); Apache-2.0 under CNCF-neutral governance; ~monthly cadence with **zero v2 breaks since v1.0 GA (2023)**; all stable modules release from one monorepo tag so MVS resolves cleanly when pinned to one train. **Caution:** `exporters/prometheus`, `contrib/otelhttp`, `contrib/otelgrpc` are *permanently* pre-1.0 by upstream policy — pin exact versions, gate bumps through review, never `@latest`. Stable family pinned to **v1.44.0**, prometheus exporter **v0.66.0**, contrib **v0.69.0**.

## Charter alignment

Upholds §1.8 (the running system is now diagnosable — latency, errors, traces, and metrics on the descent), §7 (the SLOs become measurable), §1.4 (OTel is the CNCF-standard, huge-community instrumentation layer — the boring pick), §1.7 (pinned to one evergreen train; the CI evergreen gate should group the stable family as one atomic bump and the v0.x modules as manual-review — a follow-up). No new Named Kind; no writable state; §3 already named OTel as substrate, so this realises a settled decision.

## Consequences

- **Positive:** `/metrics` works with zero collector; OTLP traces/metrics on one env var; API SLO signals immediately; Cell-attributed telemetry for multi-region.
- **Negative / trade-offs:** three permanently-pre-1.0 modules carry no SemVer contract (mitigated: exact pins + review-gated bumps). Metrics are unauthenticated on `/metrics` (standard; NetworkPolicy-scoped).
- **Follow-ups (this ADR ships the provider layer + HTTP SLOs):** domain SLO instruments (pod-spawn p95, view-query freshness, run-outcome counters) wired at their boundaries; `otelgrpc` on the sovereign plugin-port dials for trace propagation into plugins; the SLO gate tests running in CI (crack SLO-1); the CI evergreen group for the OTel train.

## Alternatives considered

- **Hand-rolled `client_golang` + no tracing** — rejected: OTel is already §3 substrate and gives one vendor-neutral API across traces+metrics; a bespoke `/metrics` plus an ad-hoc tracing shim is more surface for less.
- **OTLP-only (no Prometheus `/metrics`)** — rejected: it forces a collector on every deployment; `/metrics` is the portable floor an enterprise expects and the crack explicitly names.
- **Default-on OTLP** — rejected: an unreachable default endpoint would spew exporter errors; opt-in via one env var is the honest posture.
