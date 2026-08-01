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

// TestMergeResults_CarriesOutputs: the SAME defect as the Error fold above, one field over and
// found the same way — by running the thing. An Actuator Step's typed outputs (ADR-0031, extended
// to Apply by CERT-2) were emitted by the shim, governed against the Actuator's pinned output
// contract, captured by the hub — and then dropped by this fold, which every Run passes through
// whether or not it has more than one slice.
//
// What made it expensive is that nothing failed where the value was lost. A nil json.RawMessage
// crosses Temporal as the literal `null`, which is non-empty enough to be recorded as the Step's
// output and useless enough to break every binding into it, so the first sign of trouble was a
// message about the CONSUMER two Steps later:
//
//	template path .steps.gather.outputs.csr: "csr" is not an object
//
// Adding a field to dispatch.Result means teaching this fold about it. That is the rule the Error
// case established and this one confirms.
func TestMergeResults_CarriesOutputs(t *testing.T) {
	const csr = `{"csr":"-----BEGIN CERTIFICATE REQUEST-----"}`
	got := mergeResults([]dispatch.Result{
		{Succeeded: true, PerTarget: map[string]string{}},
		{Succeeded: true, PerTarget: map[string]string{}, Outputs: []byte(csr)},
	})
	if string(got.Outputs) != csr {
		t.Fatalf("a Step's typed outputs must survive the slice fold; got %q", got.Outputs)
	}
	// A Run whose slices publish nothing must yield NO outputs — not an empty object and not
	// `null`. The downstream binding distinguishes "nothing was published" from "a value that is
	// not an object", and only the first is an ordinary Run.
	none := mergeResults([]dispatch.Result{{Succeeded: true, PerTarget: map[string]string{}}})
	if len(none.Outputs) != 0 {
		t.Fatalf("a Run that published nothing must carry no outputs; got %q", none.Outputs)
	}
	// …including when it arrives as the literal `null`, which is what a nil json.RawMessage
	// BECOMES crossing Temporal. Every len() check reads those four bytes as present, so without
	// this the empty case survives the fold, gets recorded as the Step's output, and breaks the
	// next binding with a message about the consumer.
	lit := mergeResults([]dispatch.Result{
		{Succeeded: true, PerTarget: map[string]string{}, Outputs: []byte("null")},
	})
	if len(lit.Outputs) != 0 {
		t.Fatalf("the literal null must not survive as a published output; got %q", lit.Outputs)
	}
	if hasOutputs([]byte(" null ")) || hasOutputs(nil) || hasOutputs([]byte("")) {
		t.Fatal("hasOutputs must reject nil, empty and the null literal")
	}
	if !hasOutputs([]byte(csr)) {
		t.Fatal("hasOutputs must accept a real published object")
	}
}
