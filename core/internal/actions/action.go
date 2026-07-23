// Package actions is the in-tree Action seam (charter §2.2/§2.3): a single
// typed operation shipped by a Connector — create-vm, assign-policy, revoke-cert.
//
// The Action/Actuator split is "deliberate and permanent" (§2.3): an Actuator
// interprets arbitrary tool CONTENT and produces many effects over a View's
// Entity set; an Action is ONE contracted call with an input Contract, an
// OUTPUT Contract, and idempotency/dry-run declarations. This interface is
// separate from actuators.Actuator so the distinction is enforced by the type
// system, not convention — but both satisfy dispatch.Interpreter, so Actions
// reuse the one pod-execution path (§1.4). Actions are targetless (no View) and
// run in an execution pod so credentials inject at spawn (§2.5).
//
// Core-trust-tier plumbing only; out-of-tree Actions arrive via the plugin
// Contract surfaces (the Python SDK), never this Go interface.
package actions

import (
	"encoding/json"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

// Action is one typed operation on a Connector (§2.2).
type Action interface {
	// Name is the registry key, namespaced by Connector: "awsec2/start",
	// "awsec2/create-vm". Its Contract pair lives at actions/<Name>.input and
	// actions/<Name>.output.
	Name() string
	// Idempotent reports whether re-invoking with the same inputs is a no-op
	// (§2.2). Idempotent Actions dedup at the substrate via a stable workflow id.
	Idempotent() bool
	// DryRunnable reports whether the Action supports a side-effect-free plan
	// (§2.2). When true, Prepare(params, dryRun=true) must not mutate anything.
	DryRunnable() bool
	// Prepare renders the operation into pod content; dryRun asks for a plan
	// with no side effects (an error if dryRun is requested but !DryRunnable).
	Prepare(params json.RawMessage, dryRun bool) (actuators.JobSpec, error)
	// Interpret decodes one pod stdout line into a task event + terminal result,
	// carrying the Action's typed Outputs (validated against its output Contract).
	Interpret(line []byte) (actuators.Interpreted, bool)
}

// Registry maps Action names to their implementations (built in main.go,
// injected into the orchestration Activities beside the Actuator registry).
type Registry map[string]Action
