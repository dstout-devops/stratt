// The Stratt plugin SDK (ADR-0046): the sovereign plugin port bindings, generated
// from proto/stratt/plugin/v1. Deliberately LEAN — a plugin imports this and gets
// only gRPC + protobuf, never the control-plane's dependency graph. This is what
// lets a plugin be its own build/test/CI unit (ADR-0046 discipline (b)).
module github.com/dstout-devops/stratt/sdk

go 1.25.0

require (
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)
