// The NetBox Syncer plugin (ADR-0059): NetBox (netbox-community) is an IPAM/DCIM
// source of truth. This plugin observes its REST API and projects network
// topology — `subnet` (prefixes) and `vlan` Entities plus their placement
// Relations — over the sovereign plugin port. It imports NOTHING from core/ and
// needs no third-party client: NetBox speaks clean JSON REST over net/http.
module github.com/dstout-devops/stratt/plugins/netbox

go 1.25.0

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
