package awxfacade

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The conversion table. The `faithful` column is the load-bearing one: a false there means the
// façade reports NO rrule, which is the whole point — a wrong rrule would render a firing time
// different from the one actually in force, and nothing would contradict it.
func TestScheduleRRULE(t *testing.T) {
	cases := []struct {
		cron     string
		want     string
		faithful bool
	}{
		// The shapes the shipped estate actually uses.
		{"*/15 * * * *", "FREQ=MINUTELY;INTERVAL=15", true},        // the ansible collectors
		{"0 */6 * * *", "FREQ=HOURLY;INTERVAL=6;BYMINUTE=0", true}, // cert-reconcile
		{"30 * * * *", "FREQ=HOURLY;BYMINUTE=30", true},
		{"0 3 * * *", "FREQ=DAILY;BYHOUR=3;BYMINUTE=0", true},
		{"15 2 * * 1", "FREQ=WEEKLY;BYDAY=MO;BYHOUR=2;BYMINUTE=15", true},
		{"0 6 * * 1,3,5", "FREQ=WEEKLY;BYDAY=MO,WE,FR;BYHOUR=6;BYMINUTE=0", true},
		{"0 0 * * 7", "FREQ=WEEKLY;BYDAY=SU;BYHOUR=0;BYMINUTE=0", true}, // cron accepts 7 for Sunday

		// ── NOT FAITHFUL, and each for a stated reason ────────────────────────────────────────
		// Day-of-month AND day-of-week both restricted: cron ORs them, RRULE does not. This is the
		// case that makes a general converter impossible, so it must not be guessed at.
		{"0 0 1 * 1", "", false},
		{"0 0 1 * *", "", false},   // day-of-month restricted at all
		{"0 0 * 6 *", "", false},   // month restricted
		{"0 0 * * 1-5", "", false}, // a weekday RANGE — expressible in RRULE, but not by this converter, and a converter that guesses is the thing being avoided
		{"*/5 */2 * * *", "", false},
		{"@daily", "", false},      // macro form
		{"0 0 * * * *", "", false}, // 6-field (seconds) form
		{"not a cron", "", false},
	}
	for _, c := range cases {
		got, faithful := scheduleRRULE(c.cron)
		if faithful != c.faithful {
			t.Errorf("%q: faithful=%v, want %v (got rrule %q)", c.cron, faithful, c.faithful, got)
			continue
		}
		if got != c.want {
			t.Errorf("%q: rrule = %q, want %q", c.cron, got, c.want)
		}
	}
}

// A cron with no faithful RRULE must yield a schedule with NO rrule key at all — not an empty
// string, which a client would render as "no schedule", and not an approximation.
func TestUnconvertibleCronReportsNoRRULEAndSaysWhy(t *testing.T) {
	got := triggerToSchedule(types.Trigger{Name: "month-end", Kind: "schedule", Cron: "0 0 1 * *", WorkflowName: "wf"})
	if _, present := got["rrule"]; present {
		t.Fatalf("an unconvertible cron must report NO rrule — a wrong one would render a different "+
			"firing time than the one in force, with nothing to contradict it: %v", got["rrule"])
	}
	desc, _ := got["description"].(string)
	for _, want := range []string{"0 0 1 * *", "no faithful RRULE"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the description must carry the real spec and say why rrule is absent, missing %q: %q", want, desc)
		}
	}
}

func TestConvertibleCronReportsTheRRULE(t *testing.T) {
	got := triggerToSchedule(types.Trigger{Name: "cert-reconcile", Kind: "schedule", Cron: "0 */6 * * *", WorkflowName: "cert-issue"})
	if got["rrule"] != "FREQ=HOURLY;INTERVAL=6;BYMINUTE=0" {
		t.Fatalf("rrule = %v", got["rrule"])
	}
	if got["unified_job_template"] != awxID("cert-issue") {
		t.Errorf("a schedule must point at the job template it launches, got %v", got["unified_job_template"])
	}
	if got["type"] != "schedule" || got["timezone"] != "UTC" || got["enabled"] != true {
		t.Errorf("AWX shape not held: %v", got)
	}
}

// A Run-target Trigger has no Workflow; the View is what it acts on, and the schedule must still
// point somewhere rather than at id 0.
func TestRunTargetScheduleFallsBackToItsView(t *testing.T) {
	got := triggerToSchedule(types.Trigger{Name: "collector", Kind: "schedule", Cron: "*/15 * * * *", ViewName: "web-hosts"})
	if got["unified_job_template"] != awxID("web-hosts") {
		t.Errorf("a Run-target Trigger must report its View as the target, got %v", got["unified_job_template"])
	}
}
