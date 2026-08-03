package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func evTrigger(mut func(*types.Trigger)) types.Trigger {
	t := types.Trigger{
		Name: "flap", Kind: types.TriggerEvent, Emitter: "alerts",
		When: "event.alertname == 'LinkFlap'", WorkflowName: "remediate",
	}
	mut(&t)
	return t
}

// EVERY SHIPPED TRIGGER IS THIS CASE, and it must validate exactly as it did before ADR-0162. A
// pattern nobody declared must not become a pattern nobody wanted.
func TestATriggerWithNoPatternIsUnchanged(t *testing.T) {
	if err := ValidateTrigger(evTrigger(func(*types.Trigger) {})); err != nil {
		t.Fatalf("a plain when-only Trigger must still validate: %v", err)
	}
	if err := ValidateTrigger(evTrigger(func(tr *types.Trigger) { tr.CooldownSeconds = 60 })); err != nil {
		t.Fatalf("cooldown alone needs no window: %v", err)
	}
}

// `when` asks about ONE event; `allOf` asks whether several have all happened. A Trigger declaring
// both would need a rule to combine them, and §2.4 refuses to have one.
func TestWhenAndAllOfAreMutuallyExclusive(t *testing.T) {
	both := evTrigger(func(tr *types.Trigger) {
		tr.AllOf = []string{"event.kind == 'a'", "event.kind == 'b'"}
		tr.CorrelateBy = "service"
		tr.WithinSeconds = 600
	})
	if err := ValidateTrigger(both); err == nil {
		t.Error("declaring both `when` and `allOf` must be refused")
	}
	neither := evTrigger(func(tr *types.Trigger) { tr.When = "" })
	if err := ValidateTrigger(neither); err == nil {
		t.Error("declaring neither must be refused — an event Trigger has to ask something")
	}
}

// THE HAZARD THIS EXISTS TO REMOVE. Without a correlation key, `allOf` fires when one condition
// matched some event and another matched a DIFFERENT, unrelated one — "a deploy finished somewhere
// and a health check failed somewhere" — which converges the wrong estate and looks correct doing
// it. AAP's all() leaves this to the author; here it is unavailable.
func TestAllOfWithoutCorrelateByIsRefused(t *testing.T) {
	err := ValidateTrigger(evTrigger(func(tr *types.Trigger) {
		tr.When = ""
		tr.AllOf = []string{"event.kind == 'deploy.finished'", "event.kind == 'healthcheck.failed'"}
		tr.WithinSeconds = 900
	}))
	if err == nil {
		t.Fatal("allOf without correlateBy must be refused")
	}
	for _, want := range []string{"correlateBy", "somewhere"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must explain the hazard, not just name the field; missing %q: %v", want, err)
		}
	}
}

// A pattern measured over no window is not a pattern. Five events with no window is a total, and it
// would fire once and never again; conditions correlating over all time let last March satisfy half
// of today's.
func TestPatternsRequireAWindow(t *testing.T) {
	if err := ValidateTrigger(evTrigger(func(tr *types.Trigger) { tr.Count = 5 })); err == nil {
		t.Error("count without withinSeconds must be refused")
	}
	if err := ValidateTrigger(evTrigger(func(tr *types.Trigger) {
		tr.When = ""
		tr.AllOf = []string{"event.kind == 'a'", "event.kind == 'b'"}
		tr.CorrelateBy = "service"
	})); err == nil {
		t.Error("allOf without withinSeconds must be refused")
	}
	if err := ValidateTrigger(evTrigger(func(tr *types.Trigger) {
		tr.Count = 5
		tr.WithinSeconds = 600
	})); err != nil {
		t.Errorf("count WITH a window is the whole feature: %v", err)
	}
}

// "Five of these" and "one of each of those" are different questions.
func TestCountAndAllOfAreMutuallyExclusive(t *testing.T) {
	err := ValidateTrigger(evTrigger(func(tr *types.Trigger) {
		tr.When = ""
		tr.AllOf = []string{"event.kind == 'a'", "event.kind == 'b'"}
		tr.CorrelateBy = "service"
		tr.WithinSeconds = 600
		tr.Count = 5
	}))
	if err == nil {
		t.Error("count and allOf together must be refused")
	}
}

// Every allOf condition compiles at PARSE, for the reason ADR-0018 compiles `when` there: a rule
// that stops compiling must fail its file, never silently at event time.
func TestAllOfConditionsCompileAtDeclaration(t *testing.T) {
	err := ValidateTrigger(evTrigger(func(tr *types.Trigger) {
		tr.When = ""
		tr.AllOf = []string{"event.kind == 'a'", "this is not CEL ("}
		tr.CorrelateBy = "service"
		tr.WithinSeconds = 600
	}))
	if err == nil || !strings.Contains(err.Error(), "allOf[1]") {
		t.Fatalf("a bad condition must fail its file and NAME its index: %v", err)
	}
}
