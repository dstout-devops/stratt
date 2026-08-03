// Package triggerengine evaluates ingested Emitter events against
// event-kind Triggers (charter §3: "NATS events × CEL → Workflow launches";
// ADR-0018) and launches the declared target. Delivery is at-least-once;
// launches are deduplicated by construction: the Temporal workflow id
// derives from the trigger name + event content hash, so a redelivery hits
// Temporal's already-started rejection instead of double-launching.
package triggerengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

// Engine consumes emitter events and fires matching Triggers.
type Engine struct {
	Store    *graph.Store
	Bus      *events.Bus
	Temporal client.Client
	Log      *slog.Logger
	// ResolveActuator maps a capability class to the bound provider's ACTUATOR name for a
	// Trigger that names a class rather than an Actuator (ADR-0140 D4). Nil ⇒ such a Trigger
	// fails visibly at fire time (§1.8) rather than launching against an empty actuator.
	ResolveActuator func(ctx context.Context, capability string) (string, error)

	mu       sync.Mutex
	programs map[string]*rules.Program // trigger name → compiled rule
	specs    map[string]string         // trigger name → spec fingerprint
	// Cooldown and window bookkeeping live in graph.trigger_window (ADR-0162 D2), NOT here. The
	// in-memory map this replaced reset on every restart and gave each replica its own idea of when
	// a Trigger last fired — so the storm damping an estate declared was not the one it got, and
	// nothing said so. The engine is now stateless across events.
}

// Run consumes until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	e.programs = map[string]*rules.Program{}
	e.specs = map[string]string{}
	log := e.Log.With("component", "triggerengine")
	log.Info("trigger engine started")
	return e.Bus.ConsumeEmitterEvents(ctx, "stratt-trigger-engine", func(ev types.EmitterEvent) error {
		return e.handle(ctx, log, ev)
	})
}

// handle evaluates one event against every matching event-kind Trigger.
// Rule errors are logged per trigger and never block the others; launch
// INFRASTRUCTURE failures nak the event for redelivery — the deterministic
// workflow ids make the retry idempotent, so at-least-once holds end to end
// (charter-guardian flag on ADR-0018).
func (e *Engine) handle(ctx context.Context, log *slog.Logger, ev types.EmitterEvent) error {
	triggers, err := e.Store.ListTriggers(ctx)
	if err != nil {
		return err // infrastructure: redeliver
	}
	hash := events.EventHash(ev)
	var launchErr error
	for _, t := range triggers {
		if t.Kind != types.TriggerEvent || t.Emitter != ev.Emitter {
			continue
		}
		fire, key, err := e.decide(ctx, log, t, ev)
		if err != nil {
			// A rule that cannot decide against this payload is visible and does not launch
			// (§1.8) — never a silent false.
			log.Warn("trigger decision failed", "trigger", t.Name, "error", err)
			continue
		}
		if !fire {
			continue
		}
		// The window resets and the cooldown stamps together, BEFORE the launch: a launch that
		// failed and redelivered would otherwise re-count the same event toward the next threshold.
		if err := e.Store.TriggerWindowFired(ctx, t.Name, key); err != nil {
			log.Error("trigger window reset failed", "trigger", t.Name, "error", err)
		}
		if err := e.launch(ctx, log, t, ev, hash); err != nil {
			log.Error("trigger launch failed; event will redeliver", "trigger", t.Name, "error", err)
			launchErr = err
		}
	}
	return launchErr
}

// program returns the compiled rule, recompiling when the spec changed.
func (e *Engine) program(t types.Trigger) (*rules.Program, error) {
	doc, _ := json.Marshal(t)
	fp := string(doc)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.specs[t.Name] == fp {
		return e.programs[t.Name], nil
	}
	prg, err := rules.Compile(t.When)
	if err != nil {
		return nil, err
	}
	e.programs[t.Name] = prg
	e.specs[t.Name] = fp
	return prg, nil
}

// programFor compiles and caches one expression under an explicit key, so an AllOf Trigger's
// several conditions each get their own cached Program rather than sharing the single per-Trigger
// slot `program` uses.
func (e *Engine) programFor(key, expr string) (*rules.Program, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.specs[key] == expr {
		return e.programs[key], nil
	}
	prg, err := rules.Compile(expr)
	if err != nil {
		return nil, err
	}
	e.programs[key] = prg
	e.specs[key] = expr
	return prg, nil
}

// launch fires the Trigger's declared target with a deterministic workflow
// id — the dedup axis for at-least-once delivery. The firing event's payload
// binds {{.event.x}} references in the launch params/viewParams (ADR-0024).
// workflowDAGInput builds what a Workflow-target Trigger sends into the DAG: the firing payload
// (which each Step resolves its own {{.event.x}} bindings from — ADR-0024 D2) AND the Trigger's
// declared launch inputs, substituted against that same payload (ADR-0118 D5). Pure, so it is
// testable without a Temporal client.
func workflowDAGInput(t types.Trigger, payload map[string]any, environment string) (orchestrate.DAGInput, error) {
	inputs, err := template.SubstituteParams(t.Inputs, template.Namespaces{"event": payload})
	if err != nil {
		return orchestrate.DAGInput{}, err
	}
	return orchestrate.DAGInput{
		WorkflowName: t.WorkflowName,
		Principal:    t.Principal,
		Trigger:      t.Name,
		Event:        payload,
		LaunchParams: inputs,
		// The floor's own environment (ADR-0122 D2). A Trigger declares no environment for the
		// change context: its `environments:` list selects WHETHER it fires here, which is a
		// different question from what environment the resulting Run is in (ADR-0057).
		Environment: environment,
	}, nil
}

func (e *Engine) launch(ctx context.Context, log *slog.Logger, t types.Trigger, ev types.EmitterEvent, eventHash string) error {
	opts := client.StartWorkflowOptions{
		TaskQueue: orchestrate.TaskQueue,
	}
	short := eventHash[:16]
	ns := template.Namespaces{"event": ev.Payload}
	if t.WorkflowName != "" {
		opts.ID = fmt.Sprintf("trigger-%s-%s", t.Name, short)
		// A Workflow-target Trigger's `inputs` fill the target Workflow's declared launch
		// interface (ADR-0118 D5). Until that field existed a Trigger could not parameterize a
		// Workflow AT ALL: `params` are Step fields and are correctly refused on a Workflow
		// target ("the Workflow declares its own"), so the only Workflows a Trigger could launch
		// were ones needing no inputs. Harmless while launches accepted anything; fatal once
		// `required` inputs are enforced, which is what makes this part of the same change.
		//
		// This SHARPENS ADR-0024 D2 rather than merely fixing a bug. D2 deliberately routed the
		// payload for Workflow targets through DAGInput.Event so each Step could resolve its own
		// {{.event.x}} bindings, which is still exactly what happens; what D2 did not cover is a
		// Trigger that also wants to bind the Workflow's declared inputs. Both now travel.
		//
		// Built by a pure helper so what a firing Trigger actually sends is testable without a
		// Temporal client — this package had no tests at all, which is how the gap survived.
		in, perr := workflowDAGInput(t, ev.Payload, e.Store.ActiveEnvironment())
		if perr != nil {
			// A binding that cannot resolve against THIS payload is a terminal data error: the
			// same event will never bind, so it is dropped, never redelivered (ADR-0024 D6 — a
			// poison message must not loop).
			log.Error("trigger launch-input binding failed; event dropped (not redelivered)",
				"trigger", t.Name, "workflow", t.WorkflowName, "error", perr)
			return nil
		}
		_, err := e.Temporal.ExecuteWorkflow(ctx, opts, orchestrate.RunDAG, in)
		if isAlreadyStarted(err) {
			log.Info("trigger launch deduplicated", "trigger", t.Name, "id", opts.ID)
			return nil
		}
		if err == nil {
			log.Info("trigger launched workflow", "trigger", t.Name, "workflow", t.WorkflowName, "id", opts.ID)
		}
		return err
	}

	// Run target: resolve + re-validate params, and bind viewParams, against
	// the event before launch. A missing field or a resolved contract
	// violation is a TERMINAL data problem (this payload will never bind) —
	// logged and dropped, never launched and never redelivered (a poison
	// message must not loop). Only infrastructure failures below redeliver.
	// The trigger declaration's actuator — required for a View-actuation trigger (validated at
	// declaration; no platform default, ADR-0046) — or the bound provider of the capability it
	// names (ADR-0140 D4). Resolution happens BEFORE param binding, because which Contract the
	// params are validated against belongs to the resolved Actuator.
	actuator := t.Actuator
	if t.ActuatorCapability != "" {
		if e.ResolveActuator == nil {
			log.Error("trigger names a capability but no resolver is configured; event dropped",
				"trigger", t.Name, "capability", t.ActuatorCapability)
			return nil
		}
		resolved, rerr := e.ResolveActuator(ctx, t.ActuatorCapability)
		if rerr != nil {
			log.Error("trigger capability did not resolve; event dropped (not redelivered)",
				"trigger", t.Name, "capability", t.ActuatorCapability, "error", rerr)
			return nil
		}
		actuator = resolved
	}
	params, err := contract.ResolveActuatorParamsFor(actuator, e.Store.PluginIdentityOf(ctx, actuator), t.Params, ns)
	if err != nil {
		log.Error("trigger binding failed; event dropped (not redelivered)", "trigger", t.Name, "error", err)
		return nil
	}
	viewParams, err := template.SubstituteParams(t.ViewParams, ns)
	if err != nil {
		log.Error("trigger viewParams binding failed; event dropped (not redelivered)", "trigger", t.Name, "error", err)
		return nil
	}
	opts.ID = fmt.Sprintf("trigger-%s-%s", t.Name, short)
	_, err = e.Temporal.ExecuteWorkflow(ctx, opts, orchestrate.RunAgainstView, orchestrate.RunInput{
		ViewName:        t.ViewName,
		ViewParams:      viewParams,
		Actuator:        actuator,
		FacetWriteScope: t.FacetWriteScope,
		Params:          params,
		Slices:          t.Slices,
		Principal:       t.Principal,
		CredentialRefs:  t.CredentialRefs,
		Trigger:         t.Name,
	})
	if isAlreadyStarted(err) {
		log.Info("trigger launch deduplicated", "trigger", t.Name, "id", opts.ID)
		return nil
	}
	if err == nil {
		log.Info("trigger launched run", "trigger", t.Name, "view", t.ViewName, "id", opts.ID)
	}
	return err
}

func isAlreadyStarted(err error) bool {
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	return errors.As(err, &already)
}

// ── deciding on more than one event (ADR-0162) ──────────────────────────────────────────────────
//
// decide answers the ADR's one question — given the events already seen, should this Trigger fire? —
// and returns the correlation key the caller resets on firing.
//
// The three shapes are one code path on purpose. Cooldown, "five within ten minutes" and "both of
// these" are the same question asked with different data, and splitting them into three branches
// with three pieces of bookkeeping is how they drift apart.
func (e *Engine) decide(ctx context.Context, log *slog.Logger, t types.Trigger, ev types.EmitterEvent) (bool, string, error) {
	within := time.Duration(t.WithinSeconds) * time.Second

	// Which condition did this event satisfy? -1 for a plain `when:` Trigger, where there is only
	// one and the index carries no information.
	idx := -1
	if len(t.AllOf) > 0 {
		i, err := e.matchAllOf(t, ev)
		if err != nil {
			return false, "", err
		}
		if i < 0 {
			return false, "", nil // matched none of them; not an error, just not this event
		}
		idx = i
	} else {
		prg, err := e.program(t)
		if err != nil {
			return false, "", err
		}
		match, err := prg.Eval(ev.Emitter, ev.Payload)
		if err != nil {
			return false, "", err
		}
		if !match {
			return false, "", nil
		}
	}

	// The correlation value ties events together (D4). Empty for everything that correlates on
	// nothing, which is every cooldown and count Trigger.
	key, ok := e.correlationKey(t, ev)
	if !ok {
		// The Trigger correlates and this event carries no value for the key. It joins no window —
		// see correlationKey: a shared "" bucket would fire `allOf` on events that have nothing to do
		// with each other, which is the exact mistake D4 makes unavailable.
		log.Info("trigger match not correlated; event carries no value for correlateBy",
			"trigger", t.Name, "correlateBy", t.CorrelateBy)
		return false, "", nil
	}

	w, err := e.Store.TriggerWindowAdvance(ctx, t.Name, key, within, idx)
	if err != nil {
		return false, "", err
	}

	// Cooldown, now durable and shared. Checked AFTER the advance so a suppressed event still counts
	// toward the window — an operator asking "how many did we see?" during a cooldown wants the real
	// number, not the number we happened to act on.
	if cd := time.Duration(t.CooldownSeconds) * time.Second; cd > 0 && w.LastFiredAt != nil {
		if time.Since(*w.LastFiredAt) < cd {
			log.Info("trigger match suppressed by cooldown", "trigger", t.Name, "key", key)
			return false, key, nil
		}
	}

	switch {
	case len(t.AllOf) > 0:
		// Every declared condition satisfied by some event sharing this key, inside the window.
		if len(w.Satisfied) < len(t.AllOf) {
			log.Info("trigger waiting on conditions", "trigger", t.Name, "key", key,
				"satisfied", len(w.Satisfied), "of", len(t.AllOf))
			return false, key, nil
		}
		return true, key, nil
	case t.Count > 1:
		if w.MatchCount < t.Count {
			log.Info("trigger accumulating", "trigger", t.Name, "key", key,
				"count", w.MatchCount, "of", t.Count)
			return false, key, nil
		}
		return true, key, nil
	default:
		// No pattern declared: fire on every match. EVERY TRIGGER THAT EXISTS TODAY IS THIS CASE,
		// and it must behave exactly as it did before this ADR.
		return true, key, nil
	}
}

// matchAllOf returns the INDEX of the first declared condition this event satisfies, or -1.
//
// Index, not expression text: an author editing one condition's wording must not silently satisfy a
// different slot, and the declared ORDER is the only stable identity a condition has.
func (e *Engine) matchAllOf(t types.Trigger, ev types.EmitterEvent) (int, error) {
	for i, expr := range t.AllOf {
		prg, err := e.programFor(t.Name+"#allOf."+strconv.Itoa(i), expr)
		if err != nil {
			return -1, err
		}
		match, err := prg.Eval(ev.Emitter, ev.Payload)
		if err != nil {
			return -1, err
		}
		if match {
			return i, nil
		}
	}
	return -1, nil
}

// correlationKey reads the declared CorrelateBy PATH out of the event payload. The second result is
// false when the Trigger correlates but THIS event carries no usable value — see below, it is the
// load-bearing half.
//
// A DOTTED PATH, NOT AN EXPRESSION, and ADR-0024 is why: the blessed binding here is explicit field
// lookup with no operators and nothing evaluated. A CEL expression yielding a string would be a
// second evaluation surface whose failure modes differ from `when:`'s, for a value whose only job is
// to be equal to itself.
//
// It is spelled `event.service`, the SAME namespace `when:` reads (rules.go binds `event` to the
// payload), and the prefix is required rather than optional — accepting both spellings would make
// `correlateBy: service` mean the top-level `service` key for one payload shape and nothing at all
// for another, and the nothing-at-all case is the dangerous one below.
//
// ── AN EVENT WITH NO VALUE FOR THE KEY IS NOT CORRELATED, IT IS EXCLUDED ─────────────────────────
//
// Yielding "" for an absent field would put every such event into ONE SHARED window, which is
// precisely the hazard D4 exists to remove: 'a deploy finished somewhere and a health check failed
// somewhere' would fire, having been told the two were about the same service by a key that was
// missing from both. Not-correlated must mean not-participating, never all-in-one-bucket.
func (e *Engine) correlationKey(t types.Trigger, ev types.EmitterEvent) (string, bool) {
	if t.CorrelateBy == "" {
		return "", true // correlates on nothing: one window, which is what cooldown and count want
	}
	var cur any = ev.Payload
	for _, seg := range strings.Split(strings.TrimPrefix(t.CorrelateBy, "event."), ".") {
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[seg]
		if !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		if v == "" {
			return "", false // present but empty is no more an identity than absent
		}
		return v, true
	case bool:
		return fmt.Sprintf("%t", v), true
	case float64:
		return fmt.Sprintf("%v", v), true
	default:
		// An object or array is not an identity. Grouping by one would make every event with a
		// differently-ordered map land in a different window, which is worse than not grouping.
		return "", false
	}
}
