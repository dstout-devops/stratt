// The openbao plugin (ADR-0046 Phase B / ADR-0098) — the tool-named home for the
// OpenBao-backed surfaces, its own build/test/CI unit. It implements the NEUTRAL
// cert-issuer Contract (§1.5 — a step-ca plugin could implement the same Contract):
// a Syncer (Observe issued certs) + a reconcile Actuator (Plan/Apply/Destroy the cert
// lifecycle via born-on-target CSR/sign, ADR-0050). Future OpenBao surfaces (KV
// metadata Syncer, Transit-adjacent) consolidate here. It imports the lean plugin SDK
// and the Go standard-library HTTP client, and NOTHING from core/: the OpenBao
// transport no longer touches the control plane's dependency graph (ADR-0046).
//
// The plugin holds no graph write path (§1.2): it proposes typed ObservedEntity /
// InvokeResult values on the wire; the core-side host governs what it may write
// (ownership, identity gating, Run provenance). The CLM token is a spawn-time
// CredentialRef; it never crosses the core and is never echoed (§2.5, §1.8).
module github.com/dstout-devops/stratt/plugins/openbao

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
