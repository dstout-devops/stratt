// The awx Connector plugin: a Syncer that reads an AWX/AAP Controller's /api/v2 as a
// system-of-record and PROJECTS its automation estate — job templates, workflows,
// schedules, organizations, teams — into the graph as `ansible.*` ObservedEntities
// (§1.2, a read-only mirror; AWX stays authoritative and keeps executing). This is the
// "run Stratt beside AWX" path: we never import — the projection is always-on, we are
// connected and simply know. `stratt adopt` is the deliberate act that takes authority
// over an already-observed object (→ a Stratt-executed Named Kind). Its own build/test/CI unit;
// imports the lean plugin SDK and NOTHING from core/ (module isolation, ADR-0046). The
// plugin holds no graph write path — it proposes typed ObservedEntity values; the
// core-side host governs writes.
module github.com/dstout-devops/stratt/plugins/awx

go 1.26.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
)

require (
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
