package orchestrate

import (
	"testing"

	"github.com/dstout-devops/stratt/core/internal/dispatch"
)

// TestMergeResults_CarriesTheFailureCause: the slice fold carried Succeeded but
// dropped Error, so a Run that failed for a reason a slice had ALREADY reported
// landed on the API as status=failed with no cause — the descent said "failed" and
// stopped talking (§1.8). Surfaced by the app-cert demo asserting that its
// vacuous-run guard names WHY it failed, not merely that it did.
func TestMergeResults_CarriesTheFailureCause(t *testing.T) {
	const cause = "ansible-runner rc=0 but the play actuated no target: the play's hosts pattern matched nothing"
	got := mergeResults([]dispatch.Result{
		{Succeeded: false, Error: cause, PerTarget: map[string]string{}},
	})
	if got.Succeeded {
		t.Fatal("a failed slice must fold to a failed Run")
	}
	if got.Error != cause {
		t.Fatalf("the cause must survive the fold; got %q", got.Error)
	}
}

// A green Run has nothing to explain, and the first real cause wins over later
// silence — an empty Error from a slice that succeeded must not erase it.
func TestMergeResults_FirstCauseWinsAndSuccessStaysQuiet(t *testing.T) {
	got := mergeResults([]dispatch.Result{
		{Succeeded: true, PerTarget: map[string]string{"a": "ok"}},
		{Succeeded: false, Error: "first cause", PerTarget: map[string]string{"b": "failed"}},
		{Succeeded: false, Error: "second cause", PerTarget: map[string]string{"c": "failed"}},
	})
	if got.Error != "first cause" {
		t.Fatalf("expected the first reported cause, got %q", got.Error)
	}
	clean := mergeResults([]dispatch.Result{{Succeeded: true, PerTarget: map[string]string{"a": "ok"}}})
	if clean.Error != "" {
		t.Fatalf("a successful Run must record no cause, got %q", clean.Error)
	}
}
