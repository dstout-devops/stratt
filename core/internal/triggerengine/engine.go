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

	mu       sync.Mutex
	programs map[string]*rules.Program // trigger name → compiled rule
	specs    map[string]string         // trigger name → spec fingerprint
	lastFire map[string]time.Time      // cooldown bookkeeping (in-memory —
	// single-replica posture, ADR-0013; a restart resets cooldowns)
}

// Run consumes until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	e.programs = map[string]*rules.Program{}
	e.specs = map[string]string{}
	e.lastFire = map[string]time.Time{}
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
		prg, err := e.program(t)
		if err != nil {
			// Should be unreachable (CEL compiles at declaration parse) —
			// but a rule that stops compiling must be loud, not silent.
			log.Error("trigger rule failed to compile", "trigger", t.Name, "error", err)
			continue
		}
		match, err := prg.Eval(ev.Emitter, ev.Payload)
		if err != nil {
			// A rule that cannot decide against this payload is visible
			// and does not launch (§1.8) — never a silent false.
			log.Warn("trigger rule evaluation error", "trigger", t.Name, "error", err)
			continue
		}
		if !match {
			continue
		}
		if cd := time.Duration(t.CooldownSeconds) * time.Second; cd > 0 {
			e.mu.Lock()
			last, ok := e.lastFire[t.Name]
			suppressed := ok && time.Since(last) < cd
			if !suppressed {
				e.lastFire[t.Name] = time.Now()
			}
			e.mu.Unlock()
			if suppressed {
				log.Info("trigger match suppressed by cooldown", "trigger", t.Name)
				continue
			}
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
	// The trigger declaration's actuator — required for a View-actuation trigger
	// (validated at declaration; no platform default, ADR-0046).
	params, err := contract.ResolveActuatorParamsFor(t.Actuator, e.Store.PluginIdentityOf(ctx, t.Actuator), t.Params, ns)
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
		Actuator:        t.Actuator,
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
