package certissuer

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func TestDeclarations(t *testing.T) {
	if Issue().Name() != "certissuer/issue" || Issue().Idempotent() || !Issue().DryRunnable() {
		t.Fatalf("issue declaration: %+v", Issue())
	}
	if !Revoke().Idempotent() {
		t.Fatal("revoke must be idempotent (§2.2)")
	}
	if Renew().Idempotent() {
		t.Fatal("renew must not be idempotent (each call mints a new cert)")
	}
}

func TestPrepareValidates(t *testing.T) {
	bad := []struct {
		a   Action
		raw string
	}{
		{Issue(), `{"addr":"http://x"}`},           // missing role/commonName
		{Revoke(), `{"addr":"http://x"}`},          // missing serial
		{Issue(), `{"role":"r","commonName":"c"}`}, // missing addr
	}
	for _, c := range bad {
		if _, err := c.a.Prepare(json.RawMessage(c.raw), false); err == nil {
			t.Errorf("%s(%s): expected rejection", c.a.Name(), c.raw)
		}
	}
	spec, err := Revoke().Prepare(json.RawMessage(`{"addr":"http://x","serial":"2a:9a"}`), false)
	if err != nil || spec.Files["project/driver.py"] == "" || len(spec.Command) == 0 {
		t.Fatalf("valid revoke prepare: %v %+v", err, spec)
	}
}

func TestInterpretIssued(t *testing.T) {
	line := []byte(`{"counter":1,"event":"cert_issued","host":"web","ok":true,"outputs":{"serial":"aa:bb","notAfter":"x"}}`)
	iv, ok := Issue().Interpret(line)
	if !ok || iv.Event.Kind != "cert_issued" || len(iv.Outputs) == 0 {
		t.Fatalf("interpret: ok=%v ev=%+v outputs=%s", ok, iv.Event, iv.Outputs)
	}
	if iv.Result == nil || iv.Result.Status != actuators.StatusChanged {
		t.Fatalf("result: %+v", iv.Result)
	}
	var out map[string]any
	if json.Unmarshal(iv.Outputs, &out); out["serial"] != "aa:bb" {
		t.Fatalf("outputs: %v", out)
	}
}

func TestInterpretFailedTerminal(t *testing.T) {
	iv, ok := Revoke().Interpret([]byte(`{"counter":1,"event":"cert_failed","host":"cert","ok":false,"detail":"HTTPError"}`))
	if !ok || iv.Result == nil || !iv.Result.Failed {
		t.Fatalf("failed op must be terminal failure: %+v", iv)
	}
}

func TestInterpretNonEvent(t *testing.T) {
	if _, ok := Issue().Interpret([]byte("noise")); ok {
		t.Fatal("non-event must return ok=false")
	}
}
