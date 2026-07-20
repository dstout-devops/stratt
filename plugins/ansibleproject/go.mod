// The ansible-project Syncer plugin — "Ansible without AWX". Its system-of-record is
// a raw Ansible content root (a Git checkout / mounted directory of playbooks, roles,
// requirements.yml, and inventory files) and it PROJECTS that content into the graph
// as read-only `ansible.*` ObservedEntities (§1.2 — Git stays authoritative; nothing is
// written back, nothing is executed). It is the PRIMITIVE half of the `ansible` domain,
// of which the AWX Connector is the orchestration half: both co-project `ansible.*`, so
// a shop that never bought AWX still gets the graph, governance Baselines, and (once an
// observed artifact is ADOPTED — `stratt adopt`, never an import) Stratt-run execution.
// We never import: the projection is always-on; adopt takes authority over what we
// already know. Its own build/test/CI
// unit; imports the lean plugin SDK and NOTHING from core/ (module isolation, ADR-0046).
module github.com/dstout-devops/stratt/plugins/ansibleproject

go 1.26.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.44.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
