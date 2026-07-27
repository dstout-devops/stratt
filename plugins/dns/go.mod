// The DNS Connector/Actuator plugin (ADR-0144): it AXFRs an estate's declared zones
// into the graph as reach coordinates, and writes records by RFC 2136 dynamic update
// so a name declared in Git becomes a fact Stratt CAUSED. It imports NOTHING from
// core/; its one third-party dependency is miekg/dns, the de-facto Go DNS library
// (BSD-3-Clause, vendored by CoreDNS/external-dns/cert-manager).
module github.com/dstout-devops/stratt/plugins/dns

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	github.com/miekg/dns v1.1.72
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	golang.org/x/mod v0.36.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/tools v0.45.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
