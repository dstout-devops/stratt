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
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
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
