package awxfacade

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// ── schedules: the AWX Schedules resource, served from Triggers ─────────────────────────────────
//
// The façade is a strangler-fig front door (ADR-0026): existing AWX tooling must survive cutover,
// and `schedules` is one of the four families awxkit reaches for that did not exist. A Trigger of
// kind `schedule` IS an AWX schedule — a cron spec plus the unified job template it launches — so
// this is a projection, not a new concept.
//
// ── THE ONE HARD PART: AWX SPEAKS RRULE, STRATT SPEAKS CRON ──────────────────────────────────────
// AWX schedules carry an iCal RRULE. Stratt Triggers carry a cron expression, which Temporal
// validates and runs. The two are NOT interconvertible in general — cron's day-of-month/day-of-week
// interaction has no RRULE equivalent, and step values on several fields compose in ways RRULE
// cannot express.
//
// So the choice is between emitting a wrong RRULE and emitting none, and it is not close. A WRONG
// rrule is the worst possible output here: awxkit and the AWX UI would render a schedule that fires
// at a different time from the one that actually fires, and nothing anywhere would contradict them.
// That is not hiding mechanism (which is the product) — it is hiding a discrepancy, which §1.8
// forbids outright.
//
// This therefore converts ONLY the subset that round-trips faithfully, and for anything else omits
// `rrule` entirely and states the cron in `description`. A caller that needs the real spec can read
// it; a caller that reads rrule gets a correct answer or no answer, never a plausible wrong one.
//
// READ-ONLY, deliberately and for the same reason `job_templates` is. A schedule is a DECLARATION
// (ADR-0010): it lives in Git, is reviewed there, and is applied by the reconcile. Accepting POST
// here would make the compat surface a second write path into desired state — the imperative door
// §2.2/§2.3 keeps shut, opened by the back.

// scheduleRRULE converts a cron spec to an iCal RRULE, reporting whether the conversion is FAITHFUL.
//
// Returning `false` is a first-class outcome, not a failure: it means "this cron says something
// RRULE cannot", and the caller omits the field rather than approximating it.
func scheduleRRULE(cron string) (string, bool) {
	f := strings.Fields(cron)
	if len(f) != 5 {
		return "", false // 6-field (seconds) and @-macros are not handled; say so by saying nothing
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]

	// Anything constraining month or day-of-month is out of scope: those are exactly the fields
	// whose cron semantics (OR, not AND, when both dom and dow are restricted) RRULE does not share.
	if month != "*" || (dom != "*" && dow != "*") {
		return "", false
	}

	// */N in the minutes field, every hour, every day.
	if n, ok := stepOf(minute); ok && hour == "*" && dom == "*" && dow == "*" {
		return fmt.Sprintf("FREQ=MINUTELY;INTERVAL=%d", n), true
	}
	// A fixed minute, */N hours.
	if m, ok := fixedOf(minute); ok {
		if n, ok := stepOf(hour); ok && dom == "*" && dow == "*" {
			return fmt.Sprintf("FREQ=HOURLY;INTERVAL=%d;BYMINUTE=%d", n, m), true
		}
		// A fixed minute, every hour.
		if hour == "*" && dom == "*" && dow == "*" {
			return fmt.Sprintf("FREQ=HOURLY;BYMINUTE=%d", m), true
		}
		if h, ok := fixedOf(hour); ok {
			// A fixed time, every day.
			if dom == "*" && dow == "*" {
				return fmt.Sprintf("FREQ=DAILY;BYHOUR=%d;BYMINUTE=%d", h, m), true
			}
			// A fixed time on named weekdays.
			if days, ok := byDay(dow); ok && dom == "*" {
				return fmt.Sprintf("FREQ=WEEKLY;BYDAY=%s;BYHOUR=%d;BYMINUTE=%d", days, h, m), true
			}
		}
	}
	return "", false
}

func fixedOf(field string) (int, bool) {
	n, err := strconv.Atoi(field)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func stepOf(field string) (int, bool) {
	rest, ok := strings.CutPrefix(field, "*/")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// byDay maps a cron day-of-week list to RRULE BYDAY. Only plain numeric lists convert; ranges and
// steps are left to the not-faithful path rather than guessed at.
func byDay(dow string) (string, bool) {
	names := [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	var out []string
	for _, part := range strings.Split(dow, ",") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 7 {
			return "", false
		}
		out = append(out, names[n%7]) // cron accepts 7 for Sunday
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, ","), true
}

// triggerToSchedule projects one Trigger onto the AWX schedule shape.
func triggerToSchedule(t types.Trigger) map[string]any {
	target := t.WorkflowName
	if target == "" {
		target = t.ViewName // a Run-target Trigger has no Workflow; the View is what it acts on
	}
	desc := fmt.Sprintf("Stratt Trigger %q (cron %q)", t.Name, t.Cron)
	out := map[string]any{
		"id":       awxID(t.Name),
		"type":     "schedule",
		"url":      "/api/v2/schedules/" + strconv.FormatInt(awxID(t.Name), 10) + "/",
		"name":     t.Name,
		"timezone": "UTC",
		// Stratt has no per-Trigger enable flag: a declared Trigger is in force wherever its
		// `environments` filter places it. Reporting `true` is the honest projection of that, and
		// the alternative — inventing a disabled state the estate cannot express — would let
		// tooling believe it had turned something off.
		"enabled": true,
		// AWX computes next_run; Temporal owns the schedule here and the façade does not query it.
		// null is the correct answer for "not computed", and it is what AWX itself returns for a
		// schedule whose next occurrence is unknown.
		"next_run":             nil,
		"unified_job_template": awxID(target),
	}
	if rrule, faithful := scheduleRRULE(t.Cron); faithful {
		out["rrule"] = rrule
		out["description"] = desc
		return out
	}
	// NO rrule rather than a wrong one. The description carries the real spec so the information is
	// not lost — only the misleading rendering is.
	out["description"] = desc + " — this cron has no faithful RRULE equivalent, so none is reported " +
		"rather than an approximation that would render a different firing time than the one in force"
	return out
}

// listSchedules: GET /api/v2/schedules/ — the estate's schedule-kind Triggers.
func (f *Facade) listSchedules(w http.ResponseWriter, r *http.Request) {
	trs, err := f.cfg.Store.ListTriggers(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(trs))
	for _, t := range trs {
		if t.Kind != "schedule" {
			continue // event Triggers are not schedules; AWX has no resource for them
		}
		items = append(items, named{id: awxID(t.Name), name: t.Name, obj: triggerToSchedule(t)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getSchedules: GET /api/v2/schedules/{id}/.
func (f *Facade) getSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	trs, err := f.cfg.Store.ListTriggers(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, t := range trs {
		if t.Kind == "schedule" && awxID(t.Name) == id {
			writeJSON(w, http.StatusOK, triggerToSchedule(t))
			return
		}
	}
	awxErr(w, http.StatusNotFound, "Not found.")
}
