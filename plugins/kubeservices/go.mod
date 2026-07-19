// The kubeservices Connector plugin (ADR-0081): a Syncer that projects the service/
// capability dimension from Kubernetes Services + Helm labels — `service` Entities
// (service.endpoint), `application` Entities (software.chart, the Helm-release
// deliverable), and the `provides` M:N edge between them. Its own build/test/CI
// unit; imports the lean plugin SDK and NOTHING from core/ (the module-isolation
// discipline of ADR-0046). The plugin holds no graph write path (§1.2): it proposes
// typed ObservedEntity/ObservedRelation values; the core-side host governs writes.
module github.com/dstout-devops/stratt/plugins/kubeservices

go 1.25.0

require github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
