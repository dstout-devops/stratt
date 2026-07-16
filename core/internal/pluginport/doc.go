// Package pluginport is the core-side home of the sovereign plugin port
// (ADR-0046, Phase A). The wire contract lives in proto/stratt/plugin/v1 and is
// generated into core/gen/stratt/plugin/v1. This package will grow the host-side
// dispatcher that governs the typed Envelope — routing, authz, provenance,
// audit — while handing the opaque Payload to the plugin untouched.
//
// For now it carries the Phase-A round-trip EXISTENCE PROOF (roundtrip_test.go):
// a trivial in-memory plugin exercised over a real gRPC connection through the
// full envelope, with ZERO payload interpretation by the core side. It pins the
// load-bearing invariants concretely rather than only in prose.
package pluginport
