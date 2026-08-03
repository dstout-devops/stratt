package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A Gate decision is AUDIT EVIDENCE, so a malformed one must be refused rather than interpreted.
//
// FOUND BY TYPING IT WRONG. A caller sent {"approved":true} — one letter off — and encoding/json
// dropped the unknown key, defaulted `approve` to false, and the Gate was recorded DENIED against
// that caller's own Principal with a 200 back. The schema has always declared `required: [approve]`;
// nothing enforced it, because Decode does not.
//
// Denial is the SAFE DIRECTION, which is exactly why it survived — nothing was ever approved that
// should not have been. It is still a wrong answer where a refusal belongs: the record ends up
// saying a human denied a change they meant to approve.
//
// These cases run BEFORE any store access on purpose, so a nil Store is fine here. That ordering is
// itself the property — validation of the body must not depend on the Gate existing.
func TestGateDecisionRefusesABodyItWouldOtherwiseMisread(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"the typo that caused this":           {`{"approved":true}`, "approve"},
		"approve omitted entirely":            {`{"note":"looks fine"}`, "required"},
		"empty object":                        {`{}`, "required"},
		"an unknown field beside a valid one": {`{"approve":true,"aprove":true}`, "aprove"},
		"not an object":                       {`"yes"`, "invalid decision"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/gates/g-1/decision", strings.NewReader(c.body))
			(&Server{}).DecideGate(rr, r, "g-1")

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400 — this body would be READ AS A DENIAL and written to the "+
					"audit trail: %s", rr.Code, c.body)
			}
			if !strings.Contains(rr.Body.String(), c.want) {
				t.Errorf("the refusal must name what is wrong (missing %q): %s", c.want, rr.Body.String())
			}
		})
	}
}

// The valid shapes must stay valid — both callers in this repo (the UI mutation and the MCP
// decide_gate tool) send exactly these, and a stricter door that rejected them would trade a silent
// wrong answer for a loud broken one.
//
// Asserted by what does NOT happen: with a nil Store these panic at the Gate lookup, which is proof
// the body passed validation. A 400 here would mean the door got stricter than its own schema.
func TestGateDecisionAcceptsTheShapesItsCallersSend(t *testing.T) {
	for _, body := range []string{
		`{"approve":true}`,
		`{"approve":false}`,
		`{"approve":true,"note":"ship it"}`,
	} {
		t.Run(body, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected to reach the Gate lookup (and panic on the nil Store); a clean "+
						"return means %s was refused by validation", body)
				}
			}()
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/gates/g-1/decision", strings.NewReader(body))
			(&Server{}).DecideGate(rr, r, "g-1")
			if rr.Code == http.StatusBadRequest {
				t.Fatalf("a valid decision was refused: %s → %s", body, rr.Body.String())
			}
		})
	}
}
