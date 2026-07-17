package siteproto

import "testing"

// resetScope restores the single-Cell (byte-identical) names after a test that
// mutated the package-global scope — SetScope writes package vars.
func resetScope(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetScope("") })
}

// TestSetScopeLocalByteIdentical proves the built-in LocalCell (scope "") leaves
// every stream/KV name and subject byte-identical to the pre-Cells dispatch
// plane — a single-Cell estate must be unaffected by slice 6.
func TestSetScopeLocalByteIdentical(t *testing.T) {
	resetScope(t)
	SetScope("")
	checks := map[string]string{
		DispatchStream:            "STRATT_DISPATCH",
		ResultStream:              "STRATT_DISPATCH_RESULT",
		LivenessBucket:            "SITE_LIVENESS",
		DispatchSubjectPrefix:     "stratt.dispatch",
		DispatchStreamSubjects:    "stratt.dispatch.*",
		ResultStreamSubjects:      "stratt.dispatchresult.>",
		DispatchSubject("edge"):   "stratt.dispatch.edge",
		CancelSubject("edge"):     "stratt.dispatch.cancel.edge",
		ResultSubject("run-1", 3): "stratt.dispatchresult.run-1.3",
		ApplyStream:               "STRATT_DISPATCH_APPLY",
		ApplyStreamSubjects:       "stratt.dispatchapply.>",
		ApplySubject("run-1", 3):  "stratt.dispatchapply.run-1.3",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("local scope not byte-identical: got %q want %q", got, want)
		}
	}
}

// TestSetScopeNamed proves a named Cell scopes every name AND that the cancel
// subject stays a 4-token subject that the dispatch-stream binding never
// captures (the invariant the original const layout guaranteed).
func TestSetScopeNamed(t *testing.T) {
	resetScope(t)
	SetScope("eu")
	checks := map[string]string{
		DispatchStream:            "STRATT_DISPATCH_EU",
		ResultStream:              "STRATT_DISPATCH_RESULT_EU",
		LivenessBucket:            "SITE_LIVENESS_EU",
		DispatchSubjectPrefix:     "stratt.eu.dispatch",
		DispatchStreamSubjects:    "stratt.eu.dispatch.*",
		ResultStreamSubjects:      "stratt.eu.dispatchresult.>",
		DispatchSubject("edge"):   "stratt.eu.dispatch.edge",
		CancelSubject("edge"):     "stratt.eu.dispatch.cancel.edge",
		ResultSubject("run-1", 3): "stratt.eu.dispatchresult.run-1.3",
		ApplyStream:               "STRATT_DISPATCH_APPLY_EU",
		ApplyStreamSubjects:       "stratt.eu.dispatchapply.>",
		ApplySubject("run-1", 3):  "stratt.eu.dispatchapply.run-1.3",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("named scope: got %q want %q", got, want)
		}
	}
	// The work-queue binding (`.*`, single token) must NOT match the 4-token
	// cancel subject — else an ephemeral cancel would be queued as work.
	if DispatchStreamSubjects != DispatchSubjectPrefix+".*" {
		t.Fatal("dispatch stream binding drifted from its subject root")
	}
}

// TestSetScopeHubAgentAgree is the load-bearing invariant of slice 6: the hub
// (strattd) and a Site agent each call SetScope independently, and identical
// scope tokens MUST yield byte-identical subjects, or the two ends publish and
// subscribe on different subjects and silently talk past each other.
func TestSetScopeHubAgentAgree(t *testing.T) {
	resetScope(t)

	SetScope("eu") // hub
	hubDispatch := DispatchSubject("edge-west")
	hubResult := ResultSubject("run-9", 0)
	hubStream := DispatchStream

	SetScope("eu") // agent, same token
	if DispatchSubject("edge-west") != hubDispatch {
		t.Errorf("hub/agent dispatch subject disagree: %q vs %q", hubDispatch, DispatchSubject("edge-west"))
	}
	if ResultSubject("run-9", 0) != hubResult {
		t.Errorf("hub/agent result subject disagree")
	}
	if DispatchStream != hubStream {
		t.Errorf("hub/agent stream disagree")
	}

	// A DIFFERENT token must NOT collide with eu's subjects — the whole point.
	SetScope("us")
	if DispatchSubject("edge-west") == hubDispatch {
		t.Errorf("distinct Cells must not share a dispatch subject")
	}
}
