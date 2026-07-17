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
func (e *Engine) launch(ctx context.Context, log *slog.Logger, t types.Trigger, ev types.EmitterEvent, eventHash string) error {
	opts := client.StartWorkflowOptions{
		TaskQueue: orchestrate.TaskQueue,
	}
	short := eventHash[:16]
	ns := template.Namespaces{"event": ev.Payload}
	if t.WorkflowName != "" {
		opts.ID = fmt.Sprintf("trigger-%s-%s", t.Name, short)
		// The payload rides into the DAG; each Step resolves its own
		// {{.event.x}} bindings (ResolveStepParams activity).
		_, err := e.Temporal.ExecuteWorkflow(ctx, opts, orchestrate.RunDAG, orchestrate.DAGInput{
			WorkflowName: t.WorkflowName,
			Principal:    t.Principal,
			Trigger:      t.Name,
			Event:        ev.Payload,
		})
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
	params, err := contract.ResolveActuatorParams(t.Actuator, t.Params, ns)
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
		ViewName:       t.ViewName,
		ViewParams:     viewParams,
		Actuator:       t.Actuator,
		Params:         params,
		Slices:         t.Slices,
		Principal:      t.Principal,
		CredentialRefs: t.CredentialRefs,
		Trigger:        t.Name,
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
