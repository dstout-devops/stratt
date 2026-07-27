// Package mockstratt is the PLUGIN-FACING HOST: everything a plugin may assume
// about the thing on the other end of the sovereign port (ADR-0046), and nothing
// else. It is ADR-0137 D6's first step, and the reason that step is first — it is
// what turns D2's acid test ("developing a plugin changes nothing outside
// plugins/<name>/") from an aspiration into something a contributor can run.
//
// WHAT IT IS FOR. Not to prove a plugin works — the plugin's own tests do that.
// To prove the BOUNDARY IS REAL. A plugin that needs Postgres, NATS, Temporal,
// OpenFGA and a kind cluster to start has a dependency nobody wrote down; this
// package is the same lesson `task plugins:standalone` already taught the module
// graph, applied to the runtime. That task exists because "inside the workspace,
// go.work satisfies imports from sibling modules, so an INCOMPLETE go.sum stays
// invisible here" — convenience hiding real breakage. A monorepo full of plugins
// that can only be exercised through the control plane hides exactly the same
// class of breakage, one layer up.
//
// THE LOAD-BEARING PROPERTY: THIS HOST REFUSES WHAT THE CORE REFUSES. A mock that
// echoed whatever the plugin emitted would be worse than no mock — it would
// certify plugins that the real core drops on the floor. So the governor here is a
// faithful implementation of the core's Apply governance: the confused-deputy
// target gate, the tier+grant identity gate, the grant ∩ write-scope facet
// ceiling, derived-contract namespace confinement, and the asymmetric terminal
// fold. A plugin that passes here and fails in production means THIS package has a
// bug, and core/internal/pluginhost's parity test exists to catch that.
//
// WHAT IT DELIBERATELY DOES NOT DO. It never writes a graph, because the plugin
// never sees one — the core is the sole writer (enforce_write_path, §1.2), so a
// governed-but-unprojected result is the whole of what a plugin can observe about
// its own effects. It stamps no provenance, because a plugin cannot claim
// provenance (invariant #6) and a mock that let one is teaching a lie. It resolves
// no secret material (§2.5): CredentialRefs cross as names plus coordinates,
// exactly as they do on the wire.
//
// BOTH TRANSPORTS, ONE GOVERNOR. ADR-0051's shape is that the EE-Job (subprocess)
// and gRPC transports send the SAME ApplyRequest and are governed by the SAME
// hub-side governor. That is reproduced here rather than approximated: Subprocess
// and Conn are two ways to obtain a stream, and Host.Govern is the only thing that
// judges it. A plugin author switching transports should find that nothing about
// the verdict changes, because in the core nothing does.
//
// It imports the port bindings, gRPC, and the Go standard library. Nothing from
// core/, and no test framework — the checks return values, so a plugin's tests
// keep their own testing.T and this package stays usable from a CLI (ADR-0046
// discipline (b): a plugin is its own build/test/CI unit).
package mockstratt
