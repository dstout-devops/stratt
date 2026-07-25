package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These cover correlationLabelInParams — the guard on POST /runs {action}, the one route to real
// infrastructure with no Workflow and therefore no approval Step (§5 Flow 1, ADR-0120).
//
// The function is pure so the cases can be exhaustive: the handler needs a Store and a Temporal
// client, and a test that needed both would be skipped in `task ci` — which is how the defects this
// whole arc found stayed green.

// The motivating case: a direct Action that stamps the fleet correlation label. It builds a real
// instance AND satisfies the provisioning Finding that would otherwise have demanded an approval, so
// the gated path is not merely skipped, it is retired by the thing that skipped it.
func TestDirectActionMayNotProjectTheFleetCorrelationLabel(t *testing.T) {
	params := json.RawMessage(`{
		"region": "us-east-1",
		"projectKind": "host",
		"projectLabels": {"fleet": "web", "stratt.intent/instance": "web-02"}
	}`)
	key, bad := correlationLabelInParams(params)
	if !bad {
		t.Fatal("a direct Action projecting stratt.intent/instance must be refused")
	}
	if key != "stratt.intent/instance" {
		t.Fatalf("the refusal must name the offending key: %q", key)
	}
}

// The singleton key is the same hole with a different spelling. A prefix test rather than a list of
// two literals: a future correlation key under stratt.intent/ must be covered the day it is minted,
// not the day someone remembers to add it here.
func TestSingletonCorrelationLabelIsCaughtByTheSamePrefix(t *testing.T) {
	params := json.RawMessage(`{"projectLabels": {"stratt.intent/singleton": "Intent/Subnet/dmz-subnet"}}`)
	if key, bad := correlationLabelInParams(params); !bad || key != "stratt.intent/singleton" {
		t.Fatalf("singleton correlation must be refused too: %q %v", key, bad)
	}
}

// Depth matters, and this is the half a naive check misses. The param carrying projected labels is
// named by each plugin's own Contract, and a provider may nest it inside an opaque blob — so the walk
// must descend through maps AND arrays rather than inspecting known top-level keys.
func TestTheLabelIsFoundAtAnyDepthIncludingInsideArrays(t *testing.T) {
	for name, params := range map[string]string{
		"nested in an opaque provider blob": `{"spec": {"forProvider": {"manifest": {"metadata":
			{"labels": {"stratt.intent/instance": "web-02"}}}}}}`,
		"inside an array element": `{"relations": [{"type": "placed-in"},
			{"labels": {"stratt.intent/singleton": "Intent/Subnet/app-subnet"}}]}`,
	} {
		if _, bad := correlationLabelInParams(json.RawMessage(params)); !bad {
			t.Errorf("%s: the label must be found however deeply it is buried", name)
		}
	}
}

// It must stay QUIET on everything else, or it becomes a thing operators route around. A label that
// merely mentions the word intent, or a VALUE that looks like a correlation key, is not the reserved
// namespace — only a KEY under stratt.intent/ is.
func TestOrdinaryActionParamsAreUntouched(t *testing.T) {
	for name, params := range map[string]string{
		"no labels at all":            `{"region": "us-east-1", "instanceType": "t3.micro"}`,
		"ordinary labels":             `{"projectLabels": {"fleet": "web", "tier": "app"}}`,
		"a similar but distinct key":  `{"projectLabels": {"stratt.dev/project-kind": "host"}}`,
		"the string as a VALUE":       `{"note": "stratt.intent/instance is set by the reconcile"}`,
		"empty params":                `{}`,
		"a plain array":               `{"tags": ["a", "b"]}`,
		"intent mentioned in a key":   `{"intentNote": "x"}`,
		"nil params (no body at all)": ``,
	} {
		if key, bad := correlationLabelInParams(json.RawMessage(params)); bad {
			t.Errorf("%s: must not be refused, got %q", name, key)
		}
	}
}

// The guard must be WIRED, not merely present. Disabling the call inside startAction changed nothing
// observable when this file only tested the pure function — the exact inert-mechanism shape this arc
// keeps finding, so the wiring gets its own test.
//
// It runs against the REAL awsec2/create-vm Contract (region + ami required, projectLabels declared),
// so the request reaches the guard rather than dying at validation. Server has a nil Store and nil
// Temporal on purpose: if the guard stops returning early, the handler proceeds into LaunchRun and
// this test fails loudly instead of silently passing.
func TestStartActionRefusesACorrelationLabelBeforeLaunching(t *testing.T) {
	body := StartRun{
		Action: ptr("awsec2/create-vm"),
		Params: &map[string]any{
			"region":        "us-east-1",
			"ami":           "ami-0linuxbaseline000",
			"name":          "web-02",
			"projectKind":   "host",
			"projectLabels": map[string]any{"fleet": "web", "stratt.intent/instance": "web-02"},
		},
	}
	rr := httptest.NewRecorder()
	(&Server{}).startAction(rr, httptest.NewRequest(http.MethodPost, "/runs", nil), body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("an ungated correlated build must be refused at the door, got %d: %s", rr.Code, rr.Body.String())
	}
	for _, want := range []string{"stratt.intent/instance", "gated build", "§5 Flow 1"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("the refusal must name the label and the reason; want %q in: %s", want, rr.Body.String())
		}
	}
}

// The same route with ordinary labels must still work — this guard narrows one namespace, it does not
// close the targetless-Action door. Proven by the request getting PAST the guard: with a nil Temporal
// it cannot succeed, so the assertion is that it does not fail with the guard's 400.
func TestStartActionStillAcceptsAnUncorrelatedBuild(t *testing.T) {
	body := StartRun{
		Action: ptr("awsec2/create-vm"),
		Params: &map[string]any{
			"region":        "us-east-1",
			"ami":           "ami-0linuxbaseline000",
			"projectLabels": map[string]any{"fleet": "web"},
		},
	}
	rr := httptest.NewRecorder()
	defer func() {
		// Reaching LaunchRun with a nil Temporal is the PASS signal: the guard let it through.
		_ = recover()
		if strings.Contains(rr.Body.String(), "stratt.intent") {
			t.Errorf("an ordinary Action Run must not be refused by the correlation guard: %s", rr.Body.String())
		}
	}()
	(&Server{}).startAction(rr, httptest.NewRequest(http.MethodPost, "/runs", nil), body)
}

// Malformed JSON is the contract validator's error, raised before this runs. Reporting "no label"
// here lets that earlier, more specific failure be the one the caller sees, rather than replacing a
// precise schema pointer with a confusing note about correlation labels.
func TestMalformedParamsDeferToTheContractValidator(t *testing.T) {
	if _, bad := correlationLabelInParams(json.RawMessage(`{"broken":`)); bad {
		t.Fatal("unparseable params must not be reported as a correlation-label violation")
	}
}
