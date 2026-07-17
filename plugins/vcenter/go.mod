// The vCenter Syncer plugin (ADR-0046 Phase B) — extracted out of the control
// plane into its own build/test/CI unit. It imports the lean plugin SDK and
// govmomi, and NOTHING from core/: govmomi no longer touches the control plane's
// dependency graph (the velocity proof, ADR-0046 discipline (b)).
module github.com/dstout-devops/stratt/plugins/vcenter

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	github.com/vmware/govmomi v0.55.1
	google.golang.org/grpc v1.82.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
