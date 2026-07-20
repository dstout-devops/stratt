// The Ansible Actuator shim (ADR-0051) — the flagship's content-expertise extracted
// out of the control plane into a one-shot binary that runs INSIDE the EE image (a
// K8s Job, charter §3). It renders the inventory from the core-resolved targets, runs
// `ansible-runner` as a SUBPROCESS (the GPLv3 boundary — Ansible is never linked),
// and emits the sovereign port's typed shapes (TaskEvent / per-host ItemResult /
// ObservedEntity write-back / DiffFragment) as proto-JSON on stdout. The core
// dispatches the Job, forwards the typed stream, and governs it hub-side (ApplyRaw).
// It imports the lean sdk + the Go stdlib; NOTHING from core/, no ansible dependency.
module github.com/dstout-devops/stratt/plugins/ansible

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.82.1 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
