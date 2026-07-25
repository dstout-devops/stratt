package desiredstate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestEveryPinnableKindIsGuarded closes ADR-0119's "extend D6's guard to any future versioned CaC
// Kind by construction, rather than per-Kind".
//
// D6 is the decision that makes "immutable once pinned" true rather than aspirational: editing or
// deleting a version some Assignment pins is a plan ERROR, not an update. `guardPinnedVersions`
// implements it with an explicit `switch e.Kind` over intent and blueprint — deliberately explicit,
// because a reader of that function should be able to see which Kinds are protected without
// following a reflection trail.
//
// The risk is the omission, not the switch. Adding a third versioned CaC Kind means adding a
// `X`/`XVersion` pair to types.Assignment; forget the `case` here and a pinned version of that Kind
// becomes silently editable in place — prod changing without a pin bump, the exact failure D6
// exists to prevent, and nothing would fail. So the coverage is derived STRUCTURALLY from the
// Assignment's own pin fields, and checked BEHAVIOURALLY by running the guard.
//
// Two things make this "by construction" in the sense that matters: you cannot add a pin field
// without CI naming what is missing, and the check is a real guard invocation rather than a second
// list of Kinds that could itself go stale.
func TestEveryPinnableKindIsGuarded(t *testing.T) {
	pins := pinnableRefFields(t)
	if len(pins) < 2 {
		t.Fatalf("expected at least the intent and blueprint pins on types.Assignment, found %v — "+
			"if the pin grammar moved, this guard is looking in the wrong place and proves nothing", pins)
	}

	for _, field := range pins {
		// KindIntent == "intent", KindBlueprint == "blueprint": the plan Kind is the pin field
		// name lowercased. Asserted rather than assumed — if a future pin breaks the convention
		// the subtest fails on an unguarded Kind and the message says what to look at.
		kind := strings.ToLower(field)
		t.Run(kind, func(t *testing.T) {
			for _, act := range []Action{ActionUpdate, ActionDelete} {
				a := types.Assignment{Name: "prod-web"}
				setPin(t, &a, field, "thing", 3)

				plan := &Plan{Entries: []PlanEntry{{Kind: kind, Name: "thing@3", Action: act}}}
				if err := guardPinnedVersions(plan, Declarations{Assignments: []types.Assignment{a}}); err != nil {
					t.Fatal(err)
				}
				if plan.Entries[0].Error == "" {
					t.Errorf("types.Assignment pins %s versions, but guardPinnedVersions lets a pinned one be "+
						"%sd — add `case Kind%s:` to its switch. Without it, editing a pinned version changes "+
						"what a ring runs with no pin bump, which is the whole of ADR-0119 D6",
						kind, act, field)
					continue
				}
				// The message is the operator's only instruction here, so it must name the
				// pinning Assignment rather than only the offending document (§1.8).
				if !strings.Contains(plan.Entries[0].Error, "prod-web") {
					t.Errorf("%s %s refusal must name the Assignment doing the pinning: %s",
						kind, act, plan.Entries[0].Error)
				}
			}
		})
	}
}

// The negative half of the coverage check above, and it is not redundant with
// TestUnpinnedVersionIsFreelyEditable: that one is the end-to-end behaviour through Apply and is
// Postgres-gated (so it SKIPS in a CI without a database, which is how several defects in this repo
// stayed green) and covers Intent only. This one calls the guard directly, needs no substrate, and
// covers every pinnable Kind.
//
// It also closes a hole in the coverage test itself: a guard that refused EVERY update would satisfy
// "is this Kind guarded" while freezing the whole estate.
func TestGuardRefusesOnlyPinnedVersions(t *testing.T) {
	plan := &Plan{Entries: []PlanEntry{
		{Kind: KindIntent, Name: "scratch@1", Action: ActionUpdate},
		{Kind: KindBlueprint, Name: "scratch@1", Action: ActionDelete},
	}}
	decls := Declarations{Assignments: []types.Assignment{{
		Name: "prod-web", Intent: "other", IntentVersion: 1, Blueprint: "other", BlueprintVersion: 1,
	}}}
	if err := guardPinnedVersions(plan, decls); err != nil {
		t.Fatal(err)
	}
	for _, e := range plan.Entries {
		if e.Error != "" {
			t.Errorf("%s %s is pinned by nothing and must stay editable: %s", e.Kind, e.Name, e.Error)
		}
	}
}

// pinnableRefFields reports every `X string` field on types.Assignment that has a matching
// `XVersion int` — the structural definition of "this Kind is pinned by an Assignment", which is
// also D3's definition of versionable ("a version lives on the seam that references it").
func pinnableRefFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(types.Assignment{})
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		if v, ok := rt.FieldByName(f.Name + "Version"); ok && v.Type.Kind() == reflect.Int {
			out = append(out, f.Name)
		}
	}
	return out
}

func setPin(t *testing.T, a *types.Assignment, field, name string, version int) {
	t.Helper()
	rv := reflect.ValueOf(a).Elem()
	rv.FieldByName(field).SetString(name)
	rv.FieldByName(field + "Version").SetInt(int64(version))
}
