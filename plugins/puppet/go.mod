// The puppet (OpenVox/PuppetDB) Syncer plugin (ADR-0046/0047 Phase C) — extracted
// out of the control plane into its own build/test/CI unit. It imports the lean
// plugin SDK and talks PuppetDB over stdlib net/http + crypto/tls (mTLS); it links
// NOTHING from core/. The plugin only enumerates + normalizes; it holds no graph
// write path (ADR-0046 discipline (b): the module-isolation proof).
module github.com/dstout-devops/stratt/plugins/puppet

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
)

require (
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
