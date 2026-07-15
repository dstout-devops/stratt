package webhook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func TestPrepareRequiresBody(t *testing.T) {
	if _, err := (Actuator{}).Prepare(json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("expected error when body is missing")
	}
}

// TestPrepareCredentialMount proves the credential dir is param-driven (so
// RunAction's per-ref-name mount reaches the driver, ADR-0040).
func TestPrepareCredentialMount(t *testing.T) {
	spec, err := (Actuator{}).Prepare(json.RawMessage(`{"body":"x","credentialMount":"slack-cred"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(spec.Files["project/step.json"]), &step); err != nil {
		t.Fatal(err)
	}
	if step["credentialMount"] != "slack-cred" {
		t.Fatalf("credentialMount must thread into step.json, got %v", step["credentialMount"])
	}
}

func TestPrepareJobSpec(t *testing.T) {
	spec, err := (Actuator{}).Prepare(json.RawMessage(`{"body":"{\"hi\":1}","headers":{"X-A":"b"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Files["project/body"]; got != `{"hi":1}` {
		t.Fatalf("body file = %q", got)
	}
	if _, ok := spec.Files["project/driver.py"]; !ok {
		t.Fatal("driver.py missing")
	}
	// The driver reads the credential from a mount dir (param-driven, defaulting
	// to "webhook"), never from params (§2.5).
	if !strings.Contains(spec.Files["project/driver.py"], "/runner/credentials/") ||
		!strings.Contains(spec.Files["project/driver.py"], `step.get("credentialMount") or "webhook"`) {
		t.Fatal("driver must read the credential from the param-driven mount dir")
	}
	// The URL/token must never be baked into content (§2.5).
	for k, v := range spec.Files {
		if strings.Contains(v, "hooks.slack.com") || strings.Contains(strings.ToLower(v), "bearer ") && k != "project/driver.py" {
			t.Fatalf("file %s leaks credential material", k)
		}
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(spec.Files["project/step.json"]), &step); err != nil {
		t.Fatal(err)
	}
	if step["method"] != "POST" {
		t.Fatalf("default method = %v", step["method"])
	}
}

func TestPrepareRejectsBadMethod(t *testing.T) {
	if _, err := (Actuator{}).Prepare(json.RawMessage(`{"body":"x","method":"DELETE"}`), nil); err == nil {
		t.Fatal("expected error for unsupported method")
	}
}

func TestInterpret(t *testing.T) {
	ok, matched := (Actuator{}).Interpret([]byte(`{"counter":1,"event":"delivery_finished","host":"webhook","status":204,"ok":true}`))
	if !matched {
		t.Fatal("expected a matched event")
	}
	if ok.Result == nil || ok.Result.Failed {
		t.Fatalf("2xx must be a non-failed result: %+v", ok.Result)
	}
	if ok.Result.Status != actuators.StatusOK {
		t.Fatalf("status = %q", ok.Result.Status)
	}

	bad, _ := (Actuator{}).Interpret([]byte(`{"counter":1,"event":"delivery_finished","host":"webhook","status":500,"ok":false,"detail":"http 500"}`))
	if bad.Result == nil || !bad.Result.Failed {
		t.Fatalf("non-2xx must be a failed result: %+v", bad.Result)
	}
	if bad.Event.Payload["detail"] != "http 500" {
		t.Fatalf("detail not surfaced: %v", bad.Event.Payload)
	}

	if _, matched := (Actuator{}).Interpret([]byte(`not json`)); matched {
		t.Fatal("non-json line must not match")
	}
}
