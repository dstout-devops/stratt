// The script Actuator shim (ADR-0046/0051) — the per-target script-runner content
// extracted from the in-tree core/internal/actuators/script into a one-shot binary
// that runs INSIDE the EE image (a K8s Job, charter §3). It runs the user's script
// once per core-resolved target (sh / python3 subprocess — the tooling stays on this
// side of the port, never linked into the Apache core) and emits the sovereign port's
// typed shapes (per-target ItemResult) as proto-JSON on stdout. The core dispatches
// the Job, forwards the typed stream, and governs it hub-side (GovernStream).
module github.com/dstout-devops/stratt/plugins/script

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
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
	google.golang.org/grpc v1.82.1 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
