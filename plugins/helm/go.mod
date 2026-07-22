// The Helm Actuator plugin (ADR-0092) — helm content-expertise behind the
// sovereign plugin port (ADR-0046/0047). It runs `helm` as a SUBPROCESS (charter
// §3, transports beneath the contract §1.5; the helm binary is never linked):
// Plan renders the manifests a Gate reviews (helm template), Apply converges the
// release (helm upgrade --install, Helm-4 flags), streaming each line as a typed
// TaskEvent. Targets Helm 4 (dependency-scout: v4.2.3, N-1 v3.21.3).
//
// Lean module: the plugin SDK + gRPC/protobuf + gopkg.in/yaml.v3 (a boring,
// ubiquitous BSD-3 dep, used ONLY to redact Secret data out of rendered manifests
// before they cross the wire — §2.5; correct YAML parsing beats a line heuristic on
// a secret-bearing path). Nothing from core/, no helm Go SDK.
module github.com/dstout-devops/stratt/plugins/helm

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
	gopkg.in/yaml.v3 v3.0.1
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
