// Package notify ships the write-side Action of the notification path (charter
// §2.2, ADR-0027/0040): notify/webhook, a single contracted outbound HTTP POST.
// It reuses the webhook Actuator's pod content + interpretation, adding the
// Action envelope (name, idempotency, dry-run, input+output Contracts) so a
// notification delivery is a first-class, DESCENDABLE Run via RunAction — no
// longer a bespoke direct-dispatch inside the notifier (§1.8), and gated by the
// standard Action credential-`use` chokepoint (§2.5), not a private check.
package notify

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/actuators/webhook"
)

// WebhookAction is the notify/webhook Connector Action.
type WebhookAction struct{ act webhook.Actuator }

// Webhook constructs the Action for the registry.
func Webhook() WebhookAction { return WebhookAction{} }

// Name implements actions.Action.
func (WebhookAction) Name() string { return "notify/webhook" }

// Idempotent implements actions.Action — a POST is not a no-op; delivery dedup
// rests on the deterministic workflow id the notifier launches under.
func (WebhookAction) Idempotent() bool { return false }

// DryRunnable implements actions.Action — a webhook POST has no side-effect-free
// plan.
func (WebhookAction) DryRunnable() bool { return false }

// Prepare renders the operation into pod content, reusing the webhook Actuator.
func (a WebhookAction) Prepare(params json.RawMessage, dryRun bool) (actuators.JobSpec, error) {
	if dryRun {
		return actuators.JobSpec{}, fmt.Errorf("notify/webhook does not support dry-run")
	}
	return a.act.Prepare(params, nil)
}

// Interpret implements actions.Action / dispatch.Interpreter (the webhook
// Actuator's delivery-event interpretation; the Run status is the verdict).
func (a WebhookAction) Interpret(line []byte) (actuators.Interpreted, bool) {
	return a.act.Interpret(line)
}
