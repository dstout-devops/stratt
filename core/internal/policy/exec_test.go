package policy

import (
	"context"
	"fmt"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func fakeExec(out string, err error) Exec {
	return Exec{Name: "opa", Run: func(_ context.Context, _ string, _ []byte) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}}
}

// Exec satisfies the port — an external engine is a swappable provider (ADR-0074).
func TestExec_IsDecider(t *testing.T) {
	var _ Decider = Exec{}
}

// Decide parses the engine's Decision JSON and stamps the engine in provenance.
func TestExec_Decide(t *testing.T) {
	e := fakeExec(`{"outcome":"deny","reasons":[{"code":"rego","message":"blocked by policy"}]}`, nil)
	d := e.Decide(context.Background(), Request{})
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("engine deny must pass through, got %s", d.Outcome)
	}
	if d.Provenance.Engine != "exec:opa" {
		t.Fatalf("provenance engine = %q", d.Provenance.Engine)
	}
	if codes(d)["rego"] != 1 {
		t.Fatalf("engine reasons must pass through, got %v", d.Reasons)
	}
}

// Admit routes to the same external engine.
func TestExec_Admit(t *testing.T) {
	e := fakeExec(`{"outcome":"allow"}`, nil)
	if d := e.Admit(context.Background(), AdmissionRequest{}); d.Outcome != types.OutcomeAllow {
		t.Fatalf("engine allow must pass through, got %s", d.Outcome)
	}
}

// Every failure mode fails CLOSED to deny — never a silent allow (§1.8).
func TestExec_FailsClosed(t *testing.T) {
	cases := map[string]Exec{
		"exec error":      fakeExec("", fmt.Errorf("engine crashed")),
		"unparseable":     fakeExec("not json at all", nil),
		"invalid outcome": fakeExec(`{"outcome":"maybe"}`, nil),
		"empty output":    fakeExec("", nil),
	}
	for name, e := range cases {
		d := e.Decide(context.Background(), Request{})
		if d.Outcome != types.OutcomeDeny {
			t.Fatalf("%s: must fail closed to deny, got %s", name, d.Outcome)
		}
		if d.Provenance.Engine != "exec:opa" {
			t.Fatalf("%s: fail-closed must still stamp the engine", name)
		}
	}
}
