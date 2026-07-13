package certissuer

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func TestPrepareValidatesOperation(t *testing.T) {
	a := Actuator{}
	cases := map[string]string{
		"missing op":       `{"addr":"http://x"}`,
		"issue no cn":      `{"operation":"issue","addr":"http://x","role":"r"}`,
		"revoke no serial": `{"operation":"revoke","addr":"http://x"}`,
		"no addr":          `{"operation":"revoke","serial":"a:b"}`,
	}
	for name, raw := range cases {
		if _, err := a.Prepare(json.RawMessage(raw), nil); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	// A well-formed revoke prepares content + a python command.
	spec, err := a.Prepare(json.RawMessage(`{"operation":"revoke","addr":"http://x","serial":"2a:9a"}`), nil)
	if err != nil {
		t.Fatalf("valid revoke: %v", err)
	}
	if _, ok := spec.Files["project/driver.py"]; !ok || len(spec.Command) == 0 {
		t.Fatalf("prepared spec missing driver/command: %+v", spec)
	}
}

func TestInterpretRenewed(t *testing.T) {
	a := Actuator{}
	line := []byte(`{"counter":1,"event":"cert_renewed","host":"web.stratt.test","ok":true,"new_serial":"aa:bb","old_serial":"cc:dd"}`)
	iv, ok := a.Interpret(line)
	if !ok || iv.Event.Kind != "cert_renewed" {
		t.Fatalf("interpret: ok=%v ev=%+v", ok, iv.Event)
	}
	if iv.Event.Payload["newSerial"] != "aa:bb" || iv.Event.Payload["oldSerial"] != "cc:dd" {
		t.Fatalf("payload: %+v", iv.Event.Payload)
	}
	if iv.Result == nil || iv.Result.Status != actuators.StatusChanged || iv.Result.Failed {
		t.Fatalf("result: %+v", iv.Result)
	}
}

func TestInterpretFailedIsTerminal(t *testing.T) {
	a := Actuator{}
	iv, ok := a.Interpret([]byte(`{"counter":1,"event":"cert_failed","host":"cert-issuer","ok":false,"detail":"HTTPError"}`))
	if !ok || iv.Result == nil || !iv.Result.Failed {
		t.Fatalf("a failed op must be a terminal failure: %+v", iv)
	}
	// The sanitized detail (a class name) rides through; never a token/url.
	if iv.Event.Payload["detail"] != "HTTPError" {
		t.Fatalf("detail: %+v", iv.Event.Payload)
	}
}

func TestInterpretNonEvent(t *testing.T) {
	if _, ok := (Actuator{}).Interpret([]byte("banner noise")); ok {
		t.Fatal("non-event lines must return ok=false")
	}
}
