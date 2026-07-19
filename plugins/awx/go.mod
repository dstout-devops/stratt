// The awx Connector plugin: a Syncer that reads an AWX/AAP Controller's /api/v2 as a
// system-of-record and PROJECTS its automation estate — job templates, workflows,
// schedules, organizations, teams — into the graph as `ansible.*` ObservedEntities
// (§1.2, a read-only mirror; AWX stays authoritative and keeps executing). This is the
// "run Stratt beside AWX" path: the live projection is always-on; `stratt import awx`
// remains the deliberate convert-to-desired-state cutover. Its own build/test/CI unit;
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
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
