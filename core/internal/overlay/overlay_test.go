package overlay

import (
	"reflect"
	"testing"
)

func mustMerge(t *testing.T, layers ...Layer) (map[string]any, Provenance) {
	t.Helper()
	got, prov, err := Merge(layers)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return got, prov
}

// TestDefaultsStandWhenNotOverridden: a base layer's values survive untouched when no
// later layer sets them — the "sane defaults" half.
func TestDefaultsStandWhenNotOverridden(t *testing.T) {
	got, prov := mustMerge(t,
		Layer{Name: "base", Values: map[string]any{"port": 80, "tls": false}},
	)
	if got["port"] != 80 || got["tls"] != false {
		t.Fatalf("defaults must stand, got %v", got)
	}
	if last(prov["port"]) != "base" {
		t.Fatalf("provenance must trace to base, got %v", prov["port"])
	}
}

// TestScalarOverrideIsExplicitAndTraceable: a later layer replaces a scalar default,
// and BOTH layers are recorded — the override is never silent (§1.8 descent).
func TestScalarOverrideIsExplicitAndTraceable(t *testing.T) {
	got, prov := mustMerge(t,
		Layer{Name: "blueprint:defaults", Values: map[string]any{"port": 80}},
		Layer{Name: "overlay:prod", Values: map[string]any{"port": 443}},
	)
	if got["port"] != 443 {
		t.Fatalf("later explicit layer must win, got %v", got["port"])
	}
	// The full layering history is visible: default first, override last (effective).
	if want := []string{"blueprint:defaults", "overlay:prod"}; !reflect.DeepEqual(prov["port"], want) {
		t.Fatalf("override must record BOTH layers (history visible), got %v", prov["port"])
	}
}

// TestListsUnionAdditively: lists are the §2.4 additive claim — a later layer adds,
// never silently drops an earlier layer's elements; duplicates collapse.
func TestListsUnionAdditively(t *testing.T) {
	got, _ := mustMerge(t,
		Layer{Name: "base", Values: map[string]any{"packages": []any{"nginx", "openssl"}}},
		Layer{Name: "overlay", Values: map[string]any{"packages": []any{"openssl", "curl"}}},
	)
	want := []any{"nginx", "openssl", "curl"} // union, order-preserving, deduped
	if !reflect.DeepEqual(got["packages"], want) {
		t.Fatalf("lists must union (never drop the base), got %v", got["packages"])
	}
}

// TestDeepMapMerge: nested maps merge key-by-key — a nested default survives a sibling
// override (the reason maps recurse rather than replace wholesale).
func TestDeepMapMerge(t *testing.T) {
	got, _ := mustMerge(t,
		Layer{Name: "base", Values: map[string]any{
			"tls": map[string]any{"enabled": false, "minVersion": "1.2"},
		}},
		Layer{Name: "overlay", Values: map[string]any{
			"tls": map[string]any{"enabled": true},
		}},
	)
	tls := got["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Fatalf("nested override must apply, got %v", tls["enabled"])
	}
	if tls["minVersion"] != "1.2" {
		t.Fatalf("sibling nested default must survive, got %v", tls["minVersion"])
	}
}

// TestOrderIsTheOnlyPrecedence: reversing the layer order changes the result — proving
// precedence is the explicit structural ORDER, not any field. There is no weight to set.
func TestOrderIsTheOnlyPrecedence(t *testing.T) {
	a := Layer{Name: "a", Values: map[string]any{"x": 1}}
	b := Layer{Name: "b", Values: map[string]any{"x": 2}}
	ab, _ := mustMerge(t, a, b)
	ba, _ := mustMerge(t, b, a)
	if ab["x"] != 2 || ba["x"] != 1 {
		t.Fatalf("order is the only precedence: ab.x=%v ba.x=%v", ab["x"], ba["x"])
	}
}

// TestCrossTypeConflictFailsLoud: a layer setting a list where an earlier layer set a
// scalar (or vice versa) is a conflict, surfaced — never a silent coercion (§1.8).
func TestCrossTypeConflictFailsLoud(t *testing.T) {
	cases := [][2]Layer{
		{{Name: "base", Values: map[string]any{"x": 1}}, {Name: "over", Values: map[string]any{"x": []any{1}}}},
		{{Name: "base", Values: map[string]any{"x": []any{1}}}, {Name: "over", Values: map[string]any{"x": 1}}},
		{{Name: "base", Values: map[string]any{"x": map[string]any{"a": 1}}}, {Name: "over", Values: map[string]any{"x": 1}}},
	}
	for i, c := range cases {
		if _, _, err := Merge([]Layer{c[0], c[1]}); err == nil {
			t.Fatalf("case %d: cross-type conflict must fail loudly, not coerce", i)
		}
	}
}

// TestDeterministic: same layers → byte-identical result regardless of map iteration.
func TestDeterministic(t *testing.T) {
	layers := []Layer{
		{Name: "base", Values: map[string]any{"a": 1, "b": 2, "c": map[string]any{"d": 3, "e": 4}}},
		{Name: "over", Values: map[string]any{"b": 9, "c": map[string]any{"e": 5}}},
	}
	first, _ := mustMerge(t, layers...)
	for i := 0; i < 20; i++ {
		again, _ := mustMerge(t, layers...)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("merge not deterministic at %d", i)
		}
	}
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
