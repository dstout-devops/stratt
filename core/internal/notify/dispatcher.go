package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/types"
)

// NoticeDurable is the notice-stream durable consumer name.
const NoticeDurable = "stratt-notifier"

// Dispatcher consumes Notices and delivers them through matching Sinks. It is
// the structural twin of triggerengine.Engine: a durable, at-least-once bus
// consumer that evaluates CEL and acts. The act here is a delivery Job — the
// credential is injected into the pod at spawn, so the daemon never holds
// secret material (§2.5, ADR-0027).
type Dispatcher struct {
	Store    *graph.Store
	Bus      *events.Bus
	Temporal client.Client
	Authz    authz.Authorizer
	Log      *slog.Logger

	mu       sync.Mutex
	programs map[string]*rules.Program // subscription name → compiled match
	specs    map[string]string         // subscription name → spec fingerprint
	lastFire map[string]time.Time      // cooldown bookkeeping (in-memory,
	// single-replica posture — a restart resets cooldowns, like the engine)
}

// Run consumes until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.programs = map[string]*rules.Program{}
	d.specs = map[string]string{}
	d.lastFire = map[string]time.Time{}
	log := d.Log.With("component", "notifier")
	log.Info("notifier started")
	return d.Bus.ConsumeNotices(ctx, NoticeDurable, func(n types.Notice) error {
		return d.handle(ctx, log, n)
	})
}

// handle fans one Notice out to every matching Subscription (additive, §2.4).
// A transient delivery failure (pod could not spawn) returns an error so the
// Notice redelivers; poison per-Subscription problems (bad Sink ref, CEL
// error, endpoint rejection) are recorded on the status surface and never loop
// (§1.8 — visible, not silent; but not a redelivery storm either).
func (d *Dispatcher) handle(ctx context.Context, log *slog.Logger, n types.Notice) error {
	subs, err := d.Store.ListSubscriptions(ctx)
	if err != nil {
		return err // infrastructure: redeliver
	}
	var deliverErr error
	for _, sub := range subs {
		if !kindListed(sub.On, n.Kind) {
			continue
		}
		match, err := d.matches(sub, n)
		if err != nil {
			// A rule that cannot decide is visible and does not deliver
			// (§1.8) — never a silent false, never a launch.
			log.Warn("subscription rule error", "subscription", sub.Name, "error", err)
			continue
		}
		if !match {
			continue
		}
		if d.suppressed(sub) {
			log.Info("notification suppressed by cooldown", "subscription", sub.Name)
			continue
		}
		if err := d.deliver(ctx, log, sub, n); err != nil {
			log.Error("notification delivery failed; notice will redeliver",
				"subscription", sub.Name, "sink", sub.Sink, "error", err)
			deliverErr = err
		}
	}
	return deliverErr
}

// matches reports whether the Subscription's CEL predicate passes. An empty
// predicate matches every notice of a listed kind.
func (d *Dispatcher) matches(sub types.Subscription, n types.Notice) (bool, error) {
	if sub.Match == "" {
		return true, nil
	}
	prg, err := d.program(sub)
	if err != nil {
		return false, err
	}
	return prg.Eval(n.Kind, noticeVars(n))
}

// program returns the compiled match rule, recompiling when the spec changed.
func (d *Dispatcher) program(sub types.Subscription) (*rules.Program, error) {
	doc, _ := json.Marshal(sub)
	fp := string(doc)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.specs[sub.Name] == fp {
		return d.programs[sub.Name], nil
	}
	prg, err := rules.Compile(sub.Match)
	if err != nil {
		return nil, err
	}
	d.programs[sub.Name] = prg
	d.specs[sub.Name] = fp
	return prg, nil
}

// suppressed applies the Subscription's cooldown window (0 = none).
func (d *Dispatcher) suppressed(sub types.Subscription) bool {
	cd := time.Duration(sub.CooldownSeconds) * time.Second
	if cd <= 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	last, ok := d.lastFire[sub.Name]
	if ok && time.Since(last) < cd {
		return true
	}
	d.lastFire[sub.Name] = time.Now()
	return false
}

// deliver renders the Notice and launches the webhook Connector Action as a
// first-class, DESCENDABLE Run via RunAction (§1.8, ADR-0040) — no longer a
// bespoke direct-dispatch. The §2.5 credential-`use` check is now the standard
// Action chokepoint (RunAction.ResolveCredentials), literally shared with every
// Run (§1.6). Returns a non-nil error ONLY for transient infra failures (retry
// the whole Notice); terminal per-delivery problems are recorded and swallowed.
func (d *Dispatcher) deliver(ctx context.Context, log *slog.Logger, sub types.Subscription, n types.Notice) error {
	sink, err := d.Store.GetNotifySink(ctx, sub.Sink)
	if err != nil {
		return d.poison(ctx, log, sub, n, "sink "+sub.Sink+" not found")
	}
	if sink.Kind != types.SinkWebhook {
		return d.poison(ctx, log, sub, n, fmt.Sprintf("sink %s: unsupported kind %q", sink.Name, sink.Kind))
	}
	// Pre-flight credential-use authz: fail fast before spawning a delivery Run
	// for an ungranted Sink (RunAction re-checks at pod spawn — defense in depth).
	if err := d.authorizeSink(ctx, sink); err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}
	body, err := renderBody(sink, n)
	if err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}
	// credentialMount = the Sink's CredentialRef name: RunAction mounts the
	// secret at /runner/credentials/<name>/ and the driver reads it there.
	params, err := json.Marshal(map[string]any{
		"body": body, "method": sink.Config.Method, "credentialMount": sink.CredentialRef,
	})
	if err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}
	if err := contract.ValidateActionInput("notify/webhook", params); err != nil {
		return d.poison(ctx, log, sub, n, "contract: "+err.Error())
	}

	// A deterministic workflow id dedups a redelivered Notice: a duplicate is
	// rejected and adopted (await the prior delivery), never re-POSTed.
	wfID := "ntfy-" + sub.Name + "-" + events.NoticeHash(n)[:12]
	we, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: wfID, TaskQueue: orchestrate.TaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, orchestrate.RunAction, orchestrate.RunInput{
		Action: "notify/webhook", Params: params,
		CredentialRefs: []string{sink.CredentialRef}, Principal: sink.Principal,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &already) {
			return err // infra: could not start — redeliver
		}
		we = d.Temporal.GetWorkflow(ctx, wfID, "") // adopt the in-flight/prior delivery
	}
	var outcome orchestrate.RunOutcome
	werr := we.Get(ctx, &outcome)
	// Resolve the delivery Run by its deterministic workflow id, not from the
	// outcome — populated even when the workflow failed terminally (empty
	// outcome), so the notify_delivery → Run cross-link is never dropped exactly
	// where descent matters most (§1.8).
	runID := ""
	run, gerr := d.Store.GetRunByWorkflowID(ctx, wfID)
	if gerr == nil {
		runID = run.ID
	}
	if werr != nil {
		// The delivery Run failed terminally (authz denied, bad params, pod
		// error) — record failed, do not loop (§1.8). Never the raw error (a
		// Temporal message could carry adjacent detail).
		d.record(ctx, log, sub, sink, n, types.DeliveryFailed, "delivery run failed", runID)
		log.Error("notification delivery run failed", "sink", sink.Name, "kind", n.Kind, "run", runID, "error", werr)
		return nil
	}
	// The Run completed; a non-2xx endpoint response is RunFailed (not a workflow
	// error), so the Run status is the delivery verdict.
	if gerr == nil && run.Status == types.RunSucceeded {
		d.record(ctx, log, sub, sink, n, types.DeliveryDelivered, "", runID)
		log.Info("notification delivered", "sink", sink.Name, "kind", n.Kind, "subject", n.Subject, "run", runID)
		return nil
	}
	d.record(ctx, log, sub, sink, n, types.DeliveryFailed, "endpoint rejected the request", runID)
	log.Error("notification rejected by endpoint", "sink", sink.Name, "kind", n.Kind, "run", runID)
	return nil
}

// authorizeSink is the pre-flight credential-use check (§1.6/§2.5): a Sink cannot
// fire a credential its Principal lacks `use` on. The full resolution (backend,
// locator, mount) now lives in RunAction.ResolveCredentials — one authz model,
// literally shared with every Run.
func (d *Dispatcher) authorizeSink(ctx context.Context, sink types.Sink) error {
	if sink.CredentialRef == "" {
		return fmt.Errorf("sink %s: credentialRef is required", sink.Name)
	}
	if sink.Principal == "" {
		return fmt.Errorf("sink %s: principal is required (delivery credential use is authz-checked)", sink.Name)
	}
	allowed, err := d.Authz.Check(ctx, sink.Principal, authz.RelationUser, "credential_ref:"+sink.CredentialRef)
	if err != nil {
		return fmt.Errorf("sink %s: authz check: %w", sink.Name, err)
	}
	if !allowed {
		// Audit the denial like the shared Run path does (§1.6): a denial caught
		// at pre-flight still reaches the one audit stream (§1.8). A granted use
		// is audited by RunAction.ResolveCredentials when the delivery Run runs.
		if d.Store != nil {
			if aerr := d.Store.RecordAudit(context.WithoutCancel(ctx), types.AuditEvent{
				PrincipalID: sink.Principal, Action: types.AuditCredentialUse,
				Object: "credential_ref:" + sink.CredentialRef, Outcome: types.AuditDenied,
			}); aerr != nil {
				d.Log.Error("audit credential-use denial failed", "error", aerr)
			}
		}
		return fmt.Errorf("sink %s: principal %s lacks use on credential_ref:%s", sink.Name, sink.Principal, sink.CredentialRef)
	}
	return nil
}

// poison records a terminal per-delivery problem and returns nil (never a
// redelivery loop). The detail is a control-plane message; secret material has
// no path here.
func (d *Dispatcher) poison(ctx context.Context, log *slog.Logger, sub types.Subscription, n types.Notice, detail string) error {
	log.Error("notification not deliverable", "subscription", sub.Name, "sink", sub.Sink, "detail", detail)
	if err := d.Store.RecordDelivery(ctx, types.NotifyDelivery{
		NoticeKind: n.Kind, Subject: n.Subject, Subscription: sub.Name, Sink: sub.Sink,
		Status: types.DeliveryFailed, Detail: detail,
	}); err != nil {
		log.Error("record delivery failure failed", "error", err)
	}
	return nil
}

// record persists a delivery outcome on the status surface (§1.8).
func (d *Dispatcher) record(ctx context.Context, log *slog.Logger, sub types.Subscription, sink types.Sink, n types.Notice, status, detail, runID string) {
	if err := d.Store.RecordDelivery(ctx, types.NotifyDelivery{
		NoticeKind: n.Kind, Subject: n.Subject, Subscription: sub.Name, Sink: sink.Name,
		Status: status, Detail: detail, RunID: runID,
	}); err != nil {
		log.Error("record delivery failed", "error", err)
	}
}
