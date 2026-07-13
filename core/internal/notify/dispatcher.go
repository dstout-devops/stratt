package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dstout-devops/stratt/core/internal/actuators/webhook"
	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
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
	Store      *graph.Store
	Bus        *events.Bus
	Dispatcher *dispatch.Dispatcher
	Authz      authz.Authorizer
	Log        *slog.Logger

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

// deliver resolves the Sink + credential and dispatches one delivery Job.
// Returns a non-nil error ONLY for transient infrastructure failures (retry
// the whole Notice); terminal per-delivery problems are recorded and swallowed.
func (d *Dispatcher) deliver(ctx context.Context, log *slog.Logger, sub types.Subscription, n types.Notice) error {
	sink, err := d.Store.GetNotifySink(ctx, sub.Sink)
	if err != nil {
		return d.poison(ctx, log, sub, n, "sink "+sub.Sink+" not found")
	}
	if sink.Kind != types.SinkWebhook {
		return d.poison(ctx, log, sub, n, fmt.Sprintf("sink %s: unsupported kind %q", sink.Name, sink.Kind))
	}
	mount, err := d.resolveCredential(ctx, sink)
	if err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}
	body, err := renderBody(sink, n)
	if err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}
	raw, err := json.Marshal(map[string]any{"body": body, "method": sink.Config.Method})
	if err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}
	// Validate against the pinned webhook Contract (§2.3) before dispatch.
	if err := contract.ValidateActuatorParams(types.SinkWebhook, raw); err != nil {
		return d.poison(ctx, log, sub, n, "contract: "+err.Error())
	}
	spec, err := webhook.Actuator{}.Prepare(raw, nil)
	if err != nil {
		return d.poison(ctx, log, sub, n, err.Error())
	}

	// Deterministic delivery id: the job name is stable per (subscription,
	// notice), so a redelivery adopts the in-flight Job instead of spawning a
	// duplicate (dispatch treats AlreadyExists as adoption).
	deliveryID := fmt.Sprintf("ntfy-%s-%s", sub.Name, events.NoticeHash(n)[:12])
	res, err := d.Dispatcher.Run(ctx, deliveryID, 0, spec, webhook.Actuator{}, []dispatch.CredentialMount{mount}, nil)
	if err != nil {
		// Infrastructure: pod could not spawn / dispatch failed. Redeliver.
		return err
	}
	if res.Succeeded {
		d.record(ctx, log, sub, sink, n, types.DeliveryDelivered, "")
		log.Info("notification delivered", "sink", sink.Name, "kind", n.Kind, "subject", n.Subject)
		return nil
	}
	// The pod ran but the endpoint rejected the POST (non-2xx). Terminal for
	// v1 — record the failure on the status surface (§1.8) and do not loop
	// (a permanent 4xx must not redeliver forever). 5xx retry/backoff is a
	// documented follow-up (ADR-0027).
	d.record(ctx, log, sub, sink, n, types.DeliveryFailed, "endpoint rejected the request")
	log.Error("notification rejected by endpoint", "sink", sink.Name, "kind", n.Kind, "subject", n.Subject)
	return nil
}

// resolveCredential turns the Sink's CredentialRef into a pod mount POINTER —
// pure metadata, never material (§2.5). k8s-secret only in v1; other backends
// fail loudly. The mount is pinned under the fixed webhook mount name so the
// driver reads /runner/credentials/webhook/{url,token}.
func (d *Dispatcher) resolveCredential(ctx context.Context, sink types.Sink) (dispatch.CredentialMount, error) {
	if sink.CredentialRef == "" {
		return dispatch.CredentialMount{}, fmt.Errorf("sink %s: credentialRef is required", sink.Name)
	}
	// One authz model (§1.6): delivery runs the SAME credential-use check the
	// Run path enforces (orchestrate.ResolveCredentials), so a Sink cannot fire
	// a credential its Principal lacks `use` on — honoring the credential's
	// OwnerTeam scoping (§2.5 use-without-read).
	if sink.Principal == "" {
		return dispatch.CredentialMount{}, fmt.Errorf("sink %s: principal is required (delivery credential use is authz-checked)", sink.Name)
	}
	allowed, err := d.Authz.Check(ctx, sink.Principal, authz.RelationUser, "credential_ref:"+sink.CredentialRef)
	if err != nil {
		return dispatch.CredentialMount{}, fmt.Errorf("sink %s: authz check: %w", sink.Name, err)
	}
	if !allowed {
		return dispatch.CredentialMount{}, fmt.Errorf("sink %s: principal %s lacks use on credential_ref:%s", sink.Name, sink.Principal, sink.CredentialRef)
	}
	ref, err := d.Store.GetCredentialRef(ctx, sink.CredentialRef)
	if err != nil {
		return dispatch.CredentialMount{}, fmt.Errorf("sink %s: credentialRef %s: %w", sink.Name, sink.CredentialRef, err)
	}
	if ref.Backend != types.BackendK8sSecret {
		return dispatch.CredentialMount{}, fmt.Errorf(
			"sink %s: credentialRef %s backend %q unsupported for notification sinks in v1 (k8s-secret only)",
			sink.Name, sink.CredentialRef, ref.Backend)
	}
	var loc struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(ref.Locator, &loc); err != nil || loc.Name == "" {
		return dispatch.CredentialMount{}, fmt.Errorf("sink %s: credentialRef %s: invalid k8s-secret locator", sink.Name, sink.CredentialRef)
	}
	return dispatch.CredentialMount{
		RefName:         webhook.CredentialMountName,
		SecretNamespace: loc.Namespace,
		SecretName:      loc.Name,
		Injection:       ref.Injection,
	}, nil
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
func (d *Dispatcher) record(ctx context.Context, log *slog.Logger, sub types.Subscription, sink types.Sink, n types.Notice, status, detail string) {
	if err := d.Store.RecordDelivery(ctx, types.NotifyDelivery{
		NoticeKind: n.Kind, Subject: n.Subject, Subscription: sub.Name, Sink: sink.Name,
		Status: status, Detail: detail,
	}); err != nil {
		log.Error("record delivery failed", "error", err)
	}
}
