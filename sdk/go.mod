// The Stratt plugin SDK (ADR-0046): the sovereign plugin port bindings, generated
// from proto/stratt/plugin/v1. Deliberately LEAN — a plugin imports this and gets
// only gRPC + protobuf, never the control-plane's dependency graph. This is what
// lets a plugin be its own build/test/CI unit (ADR-0046 discipline (b)).
module github.com/dstout-devops/stratt/sdk

go 1.25.0

require (
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
)
