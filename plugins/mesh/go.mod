// The mesh Connector plugin (ADR-0082 slice 2, ADR-0081 `depends-on`): a Syncer that
// projects the RUNTIME dependency edges of the service dimension from a service-mesh's
// request telemetry — identity-anchor `service` Entities and the `service --depends-on
// --> service` M:N edge. The SECOND source of `depends-on` (a declared source is the
// other), which is why it needs relation liveness (ADR-0082): a co-asserted dependency
// stays live until every asserter drops it. Its own build/test/CI unit; imports the
// lean plugin SDK and NOTHING from core/ (module-isolation, ADR-0046) and no metrics
// client (the Prometheus transport is stdlib net/http). The plugin holds no graph write
// path (§1.2): it proposes typed values; the core-side host governs writes.
module github.com/dstout-devops/stratt/plugins/mesh

go 1.26.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
)

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
