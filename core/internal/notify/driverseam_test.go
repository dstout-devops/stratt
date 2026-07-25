package notify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/types"
)

// The decision in one assertion (ADR-0125 D1), taken on the REAL path deliver()
// uses: the Action a Sink dispatches is derived from its kind. Before this,
// deliver() ran `if sink.Kind != types.SinkWebhook { poison }` and dispatched
// the literal string "notify/webhook" — so the set of destinations Stratt could
// reach was a constant in the daemon, and "add a Slack/SMTP/PagerDuty sink" read
// as core work when it never was.
func TestSinkKindNamesItsDeliveryAction(t *testing.T) {
	webhook := types.Sink{Name: "ops", Kind: "webhook", CredentialRef: "c"}
	action, _, err := deliveryFor(webhook, "hi")
	if err != nil || action != "notify/webhook" {
		t.Fatalf("webhook sink → %q, %v", action, err)
	}

	// The one that matters: a DIFFERENT kind dispatches a different Action, off
	// the same code path, with nothing in core taught about SMTP.
	mail := types.Sink{Name: "ops-mail", Kind: "smtp", CredentialRef: "c", Config: types.SinkConfig{
		Params: map[string]any{"host": "relay.test", "from": "a@test", "to": []any{"b@test"}},
	}}
	action, params, err := deliveryFor(mail, "run failed")
	if err != nil || action != "notify/smtp" {
		t.Fatalf("smtp sink → %q, %v", action, err)
	}
	var got map[string]any
	if err := json.Unmarshal(params, &got); err != nil || got["host"] != "relay.test" {
		t.Fatalf("the driver's own params must reach its Action: %v %v", got, err)
	}
}

// deliveryParams merges the Sink's opaque driver params over the two fields core
// owns. This is what makes a per-driver knob possible without a per-driver field
// on SinkConfig — and what closes an inert mechanism: `headers` has been a
// declared property of the notify/webhook input Contract since ADR-0027 and the
// dispatcher never sent it, so a Sink could not set one and nothing failed.
func TestDeliveryParamsCarryTheDriversOwnFields(t *testing.T) {
	sink := types.Sink{Name: "ops", CredentialRef: "ops-cred", Config: types.SinkConfig{
		Params: map[string]any{"method": "PUT", "headers": map[string]any{"X-Stratt": "1"}},
	}}
	raw, err := deliveryParams(sink, `{"msg":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["body"] != `{"msg":"hi"}` || got["credentialMount"] != "ops-cred" {
		t.Errorf("core-owned fields must always be present: %v", got)
	}
	if got["method"] != "PUT" {
		t.Errorf("a driver param must reach the Action input: %v", got)
	}
	hdr, ok := got["headers"].(map[string]any)
	if !ok || hdr["X-Stratt"] != "1" {
		t.Errorf("headers must reach the driver (the previously-inert field): %v", got["headers"])
	}
	// And the driver's own pinned Contract is what types it — core validated
	// nothing above, yet a bad param is still refused, by name.
	if err := contract.ValidateActionInput("notify/webhook", raw); err != nil {
		t.Errorf("a well-formed webhook delivery must satisfy the pinned Contract: %v", err)
	}
}

// §2.4: core renders the body and names the credential mount, so a Sink that
// also set them would create two declarations of one fact with no stated winner.
// It is refused rather than resolved — a silently-overridden body would also
// break the §1.8 descent trail, which claims the delivered body is the rendered
// one.
func TestDeliveryParamsRefuseShadowingACoreOwnedField(t *testing.T) {
	for _, key := range coreDeliveryKeys {
		sink := types.Sink{Name: "ops", CredentialRef: "c", Config: types.SinkConfig{
			Params: map[string]any{key: "hijacked"},
		}}
		_, err := deliveryParams(sink, "rendered")
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("params.%s must be refused by name, got %v", key, err)
		}
	}
}

// The driver's Contract does the typing core gave up (§1.5). A Sink param the
// driver does not declare is refused at the delivery door with the field named —
// which is the whole reason core can afford to be blind to these keys.
func TestAnUndeclaredDriverParamIsRefusedByTheContract(t *testing.T) {
	sink := types.Sink{Name: "ops", CredentialRef: "c", Config: types.SinkConfig{
		Params: map[string]any{"retries": 3},
	}}
	raw, err := deliveryParams(sink, "x")
	if err != nil {
		t.Fatal(err)
	}
	err = contract.ValidateActionInput(types.NotifyActionFor("webhook"), raw)
	if err == nil || !strings.Contains(err.Error(), "retries") {
		t.Fatalf("an undeclared param must be refused naming the field, got %v", err)
	}
	// And on the real path: the Sink never dispatches at all.
	sink.Kind = "webhook"
	if _, _, err := deliveryFor(sink, "x"); err == nil {
		t.Fatal("a Sink whose params its driver does not declare must not dispatch")
	}
}

// A kind with no pinned input Contract cannot deliver, and says so by Action
// name rather than failing somewhere downstream (§1.8). This is the failure an
// operator gets from a typo'd kind, so it has to be legible.
func TestAnUnprovidedKindFailsByActionName(t *testing.T) {
	sink := types.Sink{Name: "ops", Kind: "teleporter", CredentialRef: "c"}
	_, _, err := deliveryFor(sink, "x")
	if err == nil || !strings.Contains(err.Error(), "notify/teleporter") {
		t.Fatalf("an unknown kind must fail naming its Action, got %v", err)
	}
}

// The SMTP driver's Contract is the seam's proof: a Sink declares an envelope
// core has never heard of, and it validates — because the DRIVER declared it.
func TestSMTPSinkParamsValidateAgainstTheSMTPContract(t *testing.T) {
	sink := types.Sink{Name: "ops-mail", CredentialRef: "smtp-cred", Config: types.SinkConfig{
		Params: map[string]any{
			"host": "smtp.example.test", "port": 587, "tls": "starttls",
			"from": "stratt@example.test", "to": []any{"ops@example.test"},
			"subject": "Stratt notification",
		},
	}}
	raw, err := deliveryParams(sink, "run r-1 failed")
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateActionInput(types.NotifyActionFor("smtp"), raw); err != nil {
		t.Fatalf("a well-formed smtp delivery must satisfy the pinned Contract: %v", err)
	}
	// Falsifiable in the direction that matters: the SAME params are refused by
	// the webhook Contract, so the two drivers really are separately typed and
	// core is not quietly accepting anything shaped like a map.
	if err := contract.ValidateActionInput(types.NotifyActionFor("webhook"), raw); err == nil {
		t.Fatal("smtp params must NOT satisfy the webhook Contract")
	}
}
