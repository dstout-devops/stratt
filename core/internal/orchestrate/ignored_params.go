package orchestrate

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/dstout-devops/stratt/types"
)

// ignoredParams returns the opaque `params` keys the core SENT that the provider did not say it
// consumed (ADR-0151 D4).
//
// ── WHY THE SUBTRACTION IS DONE HERE AND NOT AT THE PORT ────────────────────────────────────────
// `params` is opaque by charter (§1.5) — typed by the resolved provider's own Contract, never by
// core — so there is nothing for the core to inspect and decide it was ignored. The only thing core
// knows is what it SENT, and the only thing the provider can tell it is what it READ. The answer is
// the difference, and computing it needs both halves in one place.
//
// That place is orchestrate rather than pluginhost, because a top-level `params` object is the
// PROVISIONING launch interface's shape (ADR-0110/0120), not the port's. The host crosses every
// class; teaching it one class's convention would be the leak this repo keeps refusing.
//
// ── SILENCE IS THE LOUDEST OUTCOME ──────────────────────────────────────────────────────────────
// A provider that declares nothing gets every param it was sent reported as ignored. That is the
// inversion the port field was designed around: `ignored_params` would have let a provider that
// drops params silently return an empty list and look perfect, which is the behaviour the field
// exists to expose.
//
// Nothing is reported when NO params were sent — with an empty `params` there is no difference to
// state, and a Run event saying so on every build would train an operator to ignore the channel.
func ignoredParams(args []byte, consumed []string) []string {
	if len(args) == 0 {
		return nil
	}
	// Only the `params` object is in scope. The rest of an Action's args — `name`, `labels`,
	// `projectKind`, `placement` — are the SHARED launch interface, typed by the Action's own
	// Contract and validated against it; an unread one there is a contract question, not an
	// opaque-passthrough question. `placement` is the instructive case: kubecompute accepts and
	// ignores it BY DESIGN (a pod is placed by the scheduler), and ADR-0123 D2 settled that it is
	// accepted rather than refused so the launch interface stays shared. Folding it in here would
	// report a designed no-op as a defect on every single build.
	var envelope struct {
		Params map[string]json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(args, &envelope); err != nil {
		// Unparseable args are not this function's failure to report: the Action itself will have
		// failed on them with a better diagnosis. Staying quiet here avoids a second, worse message
		// competing with the real one.
		return nil
	}
	if len(envelope.Params) == 0 {
		return nil
	}
	read := make(map[string]bool, len(consumed))
	for _, k := range consumed {
		read[k] = true
	}
	var out []string
	for k := range envelope.Params {
		if !read[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out) // stable output: an event payload that reorders between runs is unreadable
	return out
}

// surfaceIgnoredParams reports params the provider did not consume, as a Run event.
//
// The mirror of surfaceRejections: a rejection is something the plugin sent that the core refused,
// an ignored param is something the core sent that the plugin refused. Both are governance facts
// about a Run and both belong on the stream an operator is already watching (§1.8 — one-click
// descent), rather than in a log line nobody correlates.
//
// WARN, not error. An ignored param does not make the build wrong — kubecompute ignores AWS
// coordinates entirely legitimately, and a fleet that carries them is over-declared rather than
// broken. What must never happen is that it passes UNSAID, because the failure mode it hides is a
// green build of a wrong-shaped host after a human approved the gate.
//
// Best-effort on the bus, exactly as the Action's own diagnostics are: losing the trace is bad,
// losing the build because the trace could not be written is worse.
func (a *Activities) surfaceIgnoredParams(ctx context.Context, runID, action string, ignored []string) {
	if len(ignored) == 0 {
		return
	}
	lg := a.Log
	if lg == nil {
		lg = slog.Default()
	}
	lg.Warn("provider ignored declared params",
		"action", action, "run", runID, "params", ignored)
	if a.Bus == nil || runID == "" {
		return
	}
	if err := a.Bus.Publish(ctx, types.RunEvent{
		RunID: runID,
		Kind:  "params-ignored",
		Level: types.RunEventWarn,
		Scope: types.RunEventScopeRun,
		Payload: map[string]any{
			"action": action,
			"params": ignored,
			// Say WHY it is being reported, because the reader's first question is whether they
			// broke something. They usually have not — the estate is describing a substrate this
			// provider does not serve.
			"reason": "the resolved provider did not read these declared params — they describe a " +
				"substrate it does not serve (ADR-0151 D4). The build is not wrong; the declaration " +
				"is over-specified, and a params set nobody reads is one a binding change silently " +
				"stops honouring",
		},
	}); err != nil {
		lg.Warn("publish params-ignored RunEvent failed", "error", err)
	}
}
