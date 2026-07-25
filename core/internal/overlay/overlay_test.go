package overlay

import (
	"reflect"
	"strings"
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

// TestScalarOverrideIsExplicitAndTraceable: a declaring layer replaces a YIELDING
// default, and BOTH layers are recorded — the override is never silent (§1.8 descent).
//
// The base layer is now explicitly `Yielding` (ADR-0118 D1). That is not a test tweak:
// overriding a default is legal precisely BECAUSE a default yields by definition, and
// this test previously passed with two DECLARING layers, which is the last-writer-wins
// §2.4 forbids. Two declarations of one value now fail — see
// TestTwoDeclarationsOfOneValueFailInEitherOrder.
func TestScalarOverrideIsExplicitAndTraceable(t *testing.T) {
	got, prov := mustMerge(t,
		Layer{Name: "blueprint:defaults", Values: map[string]any{"port": 80}, Yielding: true},
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
		Layer{Name: "base", Yielding: true, Values: map[string]any{
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

// TestTwoDeclarationsOfOneValueFailInEitherOrder replaces the former
// TestOrderIsTheOnlyPrecedence, whose premise ADR-0118 D1 overturned.
//
// That test asserted `ab.x == 2 && ba.x == 1` — i.e. that layer ORDER decides which
// declaration wins. charter-guardian's V1 named that as the anti-GPO violation it is:
// LSDOU is also "explicit, documented and structural", and the charter forbids GPO
// anyway. §2.4 admits only exclusive-fails-compile or additive-union, and ADR-0083 §5
// had already mandated the former for this path — `overlay.go` simply never implemented
// it. So order is no longer precedence among declarations at all, and the property to
// pin is the SYMMETRY: the same pair fails identically whichever way round it is fed,
// which is what proves no rung order survives anywhere in the engine.
func TestTwoDeclarationsOfOneValueFailInEitherOrder(t *testing.T) {
	a := Layer{Name: "intent:app", Values: map[string]any{"x": 1}}
	b := Layer{Name: "assignment:prod", Values: map[string]any{"x": 2}}
	for _, order := range [][]Layer{{a, b}, {b, a}} {
		_, _, err := Merge(order)
		if err == nil {
			t.Fatalf("two declaring layers setting x must fail, got success (order %s then %s)",
				order[0].Name, order[1].Name)
		}
		// §1.8: the operator must be able to act on this without reading our source.
		// Both layer names and the path have to appear.
		for _, want := range []string{"intent:app", "assignment:prod", `"x"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %s so the offender is actionable; got: %v", want, err)
			}
		}
	}
}

// TestYieldingLayersDoNotClaim: defaults are not declarations, so several yielding
// layers may set one path and a declaration may still override any of them. Without
// this, "sane defaults + overrides" (ADR-0083 G6) would be impossible to express.
func TestYieldingLayersDoNotClaim(t *testing.T) {
	got, _ := mustMerge(t,
		Layer{Name: "blueprint:a/defaults", Values: map[string]any{"x": 1}, Yielding: true},
		Layer{Name: "blueprint:b/defaults", Values: map[string]any{"x": 2}, Yielding: true},
		Layer{Name: "intent:app", Values: map[string]any{"x": 3}},
	)
	if got["x"] != 3 {
		t.Fatalf("a declaration must override any number of yielding defaults, got %v", got["x"])
	}
}

// TestDefaultNeverOverwritesADeclaration pins the order-independence of a default: a
// yielding layer placed AFTER a declaration must not clobber it. Today the compiler puts
// defaults first so this cannot arise, which is exactly why it is worth a test — the
// invariant must not silently depend on caller ordering, or "defaults" would beat
// declarations the moment someone appended a defaults layer at the end.
func TestDefaultNeverOverwritesADeclaration(t *testing.T) {
	got, prov := mustMerge(t,
		Layer{Name: "intent:app", Values: map[string]any{"x": 3}},
		Layer{Name: "blueprint:defaults", Values: map[string]any{"x": 1, "y": 9}, Yielding: true},
	)
	if got["x"] != 3 {
		t.Fatalf("a yielding default must not overwrite a declaration, got %v", got["x"])
	}
	if got["y"] != 9 {
		t.Fatalf("a yielding default must still FILL an unset path, got %v", got["y"])
	}
	// Provenance's documented invariant — "the LAST entry is the effective source" —
	// must survive: a default that stood down produced nothing, so it does not appear.
	// Recording it would emit a layer that did not supply the value and would make the
	// last entry a lie. (In the normal defaults-first order the history IS visible; see
	// TestScalarOverrideIsExplicitAndTraceable.)
	if want := []string{"intent:app"}; !reflect.DeepEqual(prov["x"], want) {
		t.Errorf("only the effective layer may appear for a path a default yielded on, got %v", prov["x"])
	}
	if last(prov["y"]) != "blueprint:defaults" {
		t.Errorf("a default that DID supply a value must be recorded, got %v", prov["y"])
	}
}

// TestListsAreExemptFromTheExclusiveRule: §2.4 makes lists the ADDITIVE claim, so two
// DECLARING layers legitimately contribute elements to one list — that is a union, not a
// contest. Getting this wrong in the other direction would break the access/fileset
// Blueprints, whose whole model is many Intents unioning grants into one Facet.
func TestListsAreExemptFromTheExclusiveRule(t *testing.T) {
	got, _ := mustMerge(t,
		Layer{Name: "intent:a", Values: map[string]any{"pkgs": []any{"nginx"}}},
		Layer{Name: "intent:b", Values: map[string]any{"pkgs": []any{"curl"}}},
	)
	want := []any{"nginx", "curl"}
	if !reflect.DeepEqual(got["pkgs"], want) {
		t.Fatalf("two declaring layers must UNION a list (additive claim), got %v", got["pkgs"])
	}
}

// TestNestedDeclarationsCollide: the exclusive rule addresses leaf PATHS, not top-level
// keys, so two declarations meeting deep inside a shared map still fail. A rule that only
// guarded the first level would be trivially evaded by nesting.
func TestNestedDeclarationsCollide(t *testing.T) {
	_, _, err := Merge([]Layer{
		{Name: "intent:app", Values: map[string]any{"tls": map[string]any{"minVersion": "1.2"}}},
		{Name: "assignment:prod", Values: map[string]any{"tls": map[string]any{"minVersion": "1.3"}}},
	})
	if err == nil {
		t.Fatal("a nested double-claim must fail")
	}
	if !strings.Contains(err.Error(), "tls.minVersion") {
		t.Errorf("the error must name the full dotted path, got: %v", err)
	}
}

// TestDisjointDeclarationsMerge is the ergonomics this design trades for: two
// declarations compose freely as long as they do not overlap, which is what makes
// "omit it from the Intent to decide it per environment" workable rather than merely legal.
func TestDisjointDeclarationsMerge(t *testing.T) {
	got, _ := mustMerge(t,
		Layer{Name: "blueprint:defaults", Values: map[string]any{"channel": "stable"}, Yielding: true},
		Layer{Name: "intent:app", Values: map[string]any{"package": "nginx"}},
		Layer{Name: "assignment:prod", Values: map[string]any{"port": 443}},
	)
	if got["package"] != "nginx" || got["port"] != 443 || got["channel"] != "stable" {
		t.Fatalf("disjoint declarations plus defaults must compose, got %v", got)
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
		{Name: "base", Yielding: true, Values: map[string]any{"a": 1, "b": 2, "c": map[string]any{"d": 3, "e": 4}}},
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
