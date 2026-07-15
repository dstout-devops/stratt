package notify

import (
	"encoding/json"
	"testing"
)

// TestWebhookAction proves the notify/webhook Action envelope: the registry
// name, non-idempotent / non-dry-runnable declarations, dry-run refusal, and
// that Prepare reuses the webhook Actuator's pod content (ADR-0040).
func TestWebhookAction(t *testing.T) {
	a := Webhook()
	if a.Name() != "notify/webhook" {
		t.Fatalf("name = %q", a.Name())
	}
	if a.Idempotent() || a.DryRunnable() {
		t.Fatal("a webhook POST is neither idempotent nor dry-runnable")
	}
	if _, err := a.Prepare(json.RawMessage(`{"body":"x"}`), true); err == nil {
		t.Fatal("dry-run must be refused")
	}
	spec, err := a.Prepare(json.RawMessage(`{"body":"{\"a\":1}","credentialMount":"c"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Files["project/body"] != `{"a":1}` {
		t.Fatalf("body not rendered into pod content: %q", spec.Files["project/body"])
	}
}
