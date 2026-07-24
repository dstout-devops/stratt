package ansible

import (
	"strings"
	"testing"
)

// TestVacuousRun_ZeroActuationIsNotSuccess pins the §1.8 hole this closes:
// ansible-playbook exits 0 when a play's `hosts:` pattern matches no inventory
// host, and the hub folds Succeeded from `sawTerminal && !failed` — zero hosts
// means zero failures, so the Run would report GREEN having changed nothing.
func TestVacuousRun_ZeroActuationIsNotSuccess(t *testing.T) {
	targets := []Target{{Name: "web-02"}, {Name: "web-01"}}

	got := vacuousRun(0, targets, 0, "", false)
	if got == "" {
		t.Fatal("rc=0 with a non-empty target set and ZERO hosts actuated must FAIL, not report green")
	}
	// §1.8: the diagnosis names the resolved set (deterministically ordered) — a bare
	// "rc=0" diagnoses nothing. The CAUSE is asserted only when observed; see
	// TestVacuousMessageDoesNotAssertAnUnobservedCause.
	if !strings.Contains(got, "[web-01 web-02]") {
		t.Fatalf("message must name the resolved targets in stable order: %q", got)
	}
	if !strings.Contains(got, "not a success") {
		t.Fatalf("message must state the run is not a success: %q", got)
	}
}

// TestVacuousRun_NamesLimitWhenSet: params.limit (ADR-0117 D1) is the second live
// cause — it narrows the core-resolved set and can narrow it to EMPTY. A run that
// actuated nothing under a limit must say so, with the offending value.
func TestVacuousRun_NamesLimitWhenSet(t *testing.T) {
	got := vacuousRun(0, []Target{{Name: "web-01"}}, 0, "nonexistent", true)
	if !strings.Contains(got, `params.limit="nonexistent"`) {
		t.Fatalf("a vacuous run under a limit must name the limit value: %q", got)
	}
	if !strings.Contains(got, "no hosts matched") {
		t.Fatalf("ansible's own no-hosts-matched signal must be reported when seen: %q", got)
	}
}

// TestVacuousRun_LegitimateRunsAreUntouched: the check fires ONLY on zero
// actuation. A limit that narrows 3 targets to 1 is a requested narrowing, not a
// vacuous run — treating it as failure would break the knob shipped in slice 1.
func TestVacuousRun_LegitimateRunsAreUntouched(t *testing.T) {
	three := []Target{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	cases := map[string]string{
		"narrowed by limit":  vacuousRun(0, three, 1, "a", false),
		"all actuated":       vacuousRun(0, three, 3, "", false),
		"targetless run":     vacuousRun(0, nil, 0, "", false),
		"already failing rc": vacuousRun(2, three, 0, "", false),
	}
	for name, got := range cases {
		if got != "" {
			t.Errorf("%s must not be reported vacuous, got %q", name, got)
		}
	}
}

// TestIsNoHostsMatched pins which ansible events mean "this play targeted nothing"
// — they are surfaced at WARN so partial vacuity (one play of several no-opping)
// stays visible during descent even when the run legitimately succeeds overall.
func TestIsNoHostsMatched(t *testing.T) {
	for _, ev := range []string{"playbook_on_no_hosts_matched", "playbook_on_no_hosts_remaining"} {
		if !isNoHostsMatched(RunnerEvent{Event: ev}) {
			t.Errorf("%s must be recognized as a no-op play signal", ev)
		}
	}
	for _, ev := range []string{"runner_on_ok", "playbook_on_play_start", ""} {
		if isNoHostsMatched(RunnerEvent{Event: ev}) {
			t.Errorf("%q must not be treated as a no-op play signal", ev)
		}
	}
}
