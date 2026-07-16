// The cert-issuer Connector plugin (ADR-0046 Phase B) — extracted out of the
// control plane into its own build/test/CI unit. It ships BOTH capabilities of the
// certissuer Connector: a Syncer (Observe issued certs) and a MULTI-OP Action
// (Invoke certissuer/issue | certissuer/renew | certissuer/revoke against a
// Vault-compatible PKI CLM). It imports the lean plugin SDK and the Go standard
// library HTTP client, and NOTHING from core/: the CLM transport no longer touches
// the control plane's dependency graph (the module-isolation discipline of ADR-0046).
//
// The plugin holds no graph write path (§1.2): it proposes typed ObservedEntity /
// InvokeResult values on the wire; the core-side host governs what it may write
// (ownership, identity gating, Run provenance). The CLM token is a spawn-time
// CredentialRef; it never crosses the core and is never echoed (§2.5, §1.8).
module github.com/dstout-devops/stratt/plugins/certissuer

go 1.25.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)

require (
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260420184626-e10c466a9529 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk
