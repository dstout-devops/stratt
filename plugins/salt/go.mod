// The Salt Syncer plugin (ADR-0046/0047 Phase C) — the salt-api grains
// content-expertise extracted out of the control plane into its own
// build/test/CI unit, behind the sovereign plugin port. It imports the lean
// plugin SDK and only the Go standard library for the HTTP transport; nothing
// from core/. Syncer only — the salt Emitter stays in-tree (host does not yet
// handle the Subscribe/EmittedEvent path).
module github.com/dstout-devops/stratt/plugins/salt

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260420184626-e10c466a9529 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
