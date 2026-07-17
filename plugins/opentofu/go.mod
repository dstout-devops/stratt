// The OpenTofu Actuator plugin (ADR-0046/0047 Phase C, Actuator slice 4) — the
// tofu content-expertise extracted out of the control plane into its own
// build/test/CI unit, behind the sovereign plugin port. It is the FIRST plugin to
// implement the converge verbs (Plan/Apply/Destroy over the port): Apply streams
// tofu's -json as typed TaskEvents, lifts drift + per-workspace status, and turns
// the reserved stratt_entities output into governed write-back plus a rung-2
// DerivedContract; Plan produces the hash-pinned saved plan (ADR-0047 §7/§8).
//
// OpenTofu is a SUBPROCESS (charter §3: OpenTofu over Terraform, shelled out — the
// tofu binary is never linked). So this module imports only the lean plugin SDK
// and the Go standard library; nothing from core/, and no tofu SDK.
module github.com/dstout-devops/stratt/plugins/opentofu

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
