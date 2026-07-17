// The static-inventory Syncer plugin (ADR-0056 §5) — "devices as code". Its
// system-of-record is a host-list file in the estate repo; it projects `host`
// Entities over the sovereign plugin port and imports NOTHING from core/ (the
// module-isolation discipline, ADR-0046). The leanest possible plugin: the SDK,
// gRPC, and a YAML parser.
module github.com/dstout-devops/stratt/plugins/staticinv

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
