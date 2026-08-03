package orchestrate

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func ent(id string, labels map[string]string) types.Entity {
	return types.Entity{ID: id, Labels: labels}
}

// THE PROPERTY THIS EXISTS FOR (ADR-0161 D2): every DISTINCT value of a key becomes a group, so a
// value nobody enumerated still produces one. That is what a View-per-value cannot do and what
// `keyed_groups` is for — and it arrives with no expression language, only a key.
func TestEveryDistinctValueBecomesAGroup(t *testing.T) {
	keys := []types.GroupKey{{Label: "role"}}
	for _, c := range []struct {
		role string
		want []string
	}{
		{"web", []string{"web"}},
		{"db", []string{"db"}},
		// Nobody declared "cache" anywhere. It groups anyway.
		{"cache", []string{"cache"}},
	} {
		got, err := groupsFor(ent("e", map[string]string{"role": c.role}), nil, keys)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("role=%s: got %v (%v), want %v", c.role, got, err, c.want)
		}
	}
}

// A target with no value for a key joins NO group from it — never an "unknown" bucket. Inventing a
// bucket would be the core asserting a value the graph does not carry (§1.2), and a play targeting
// it would converge hosts on the strength of a fact nobody observed.
func TestNoValueMeansNoGroupNotAnUnknownBucket(t *testing.T) {
	got, err := groupsFor(ent("e", map[string]string{"other": "x"}), nil, []types.GroupKey{{Label: "role"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a target without the label joins nothing, got %v", got)
	}
}

// Facet keys address a value inside a document exactly as a ViewSelector addresses one — the same
// structured predicate data, asking for the value rather than testing it.
func TestFacetKeysGroupByAValueInsideTheDocument(t *testing.T) {
	facets := map[string]json.RawMessage{
		"cloud.placement": json.RawMessage(`{"region":"eu-west-1","zone":"a"}`),
	}
	got, err := groupsFor(ent("e", nil), facets, []types.GroupKey{
		{Facet: &types.FacetKey{Namespace: "cloud.placement", Path: "region"}, Prefix: "region"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"region_eu_west_1"}) {
		t.Fatalf("got %v", got)
	}
	// A path that addresses nothing yields no group rather than a group named after the miss.
	none, _ := groupsFor(ent("e", nil), facets, []types.GroupKey{
		{Facet: &types.FacetKey{Namespace: "cloud.placement", Path: "nope"}},
	})
	if len(none) != 0 {
		t.Errorf("an absent path must not group: %v", none)
	}
	// Only SCALARS group: "the group whose name is a JSON object" is not a thing anyone meant.
	obj, _ := groupsFor(ent("e", nil), facets, []types.GroupKey{
		{Facet: &types.FacetKey{Namespace: "cloud.placement"}},
	})
	if len(obj) != 0 {
		t.Errorf("an object value must not group: %v", obj)
	}
}

// A key with two sources, or none, needs a rule to pick between them — and §2.4 refuses to have one.
func TestAKeyWithTwoSourcesIsRefused(t *testing.T) {
	for name, k := range map[string]types.GroupKey{
		"both":    {Label: "role", Facet: &types.FacetKey{Namespace: "x"}},
		"neither": {Prefix: "p"},
	} {
		if _, err := groupsFor(ent("e", map[string]string{"role": "web"}), nil, []types.GroupKey{k}); err == nil {
			t.Errorf("%s: must be refused, not resolved by a precedence rule", name)
		}
	}
}

// Group names must be usable by ansible, and the transform is LOSSY — which is stated rather than
// discovered. `eu-west-1` and `eu.west.1` are different observed values that both land on
// `eu_west_1`. The merge is real; what must never happen is it being invisible (see applyGroups).
func TestGroupNamesAreSanitisedAndTheCollisionIsReal(t *testing.T) {
	for _, c := range []struct{ prefix, value, want string }{
		{"", "web", "web"},
		{"region", "eu-west-1", "region_eu_west_1"},
		{"", "eu.west.1", "eu_west_1"},
		{"", "eu-west-1", "eu_west_1"}, // ← the same name as the line above: the documented merge
		{"", "10", "g_10"},             // must not start with a digit
		{"", "!!!", ""},                // nothing usable survives ⇒ no group at all
		{"env", "PROD", "env_PROD"},    // case is preserved; ansible is case-sensitive
	} {
		if got := groupName(c.prefix, c.value); got != c.want {
			t.Errorf("groupName(%q,%q) = %q, want %q", c.prefix, c.value, got, c.want)
		}
	}
}

// Deterministic order, because the rendered inventory's byte-stability is a §1.8 property other
// tests pin: two Runs over one target set must be comparable during descent.
func TestGroupsAreSortedSoTwoRunsRenderIdentically(t *testing.T) {
	keys := []types.GroupKey{{Label: "zzz"}, {Label: "aaa"}, {Label: "mmm"}}
	e := ent("e", map[string]string{"zzz": "z", "aaa": "a", "mmm": "m"})
	first, _ := groupsFor(e, nil, keys)
	second, _ := groupsFor(e, nil, keys)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, []string{"a", "m", "z"}) {
		t.Fatalf("got %v then %v, want sorted [a m z]", first, second)
	}
}

// THE WIRING, not just the resolver. Every test above calls groupsFor directly and all of them
// would still pass if a Step's declared GroupBy never reached the Run — "a shipped mechanism nothing
// invokes" being the defect class this repo has booked repeatedly. This one reads the source of the
// Step→RunInput construction, because the alternative is a Temporal env test whose failure mode is
// the same silence.
func TestAStepsGroupByReachesTheRun(t *testing.T) {
	src, err := os.ReadFile("workflow.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	i := strings.Index(text, "RunAgainstView, RunInput{")
	if i < 0 {
		t.Fatal("the actuation Step's RunInput construction is gone — this guard has lost its subject")
	}
	block := text[i : i+900]
	if !strings.Contains(block, "GroupBy: step.GroupBy") {
		t.Error("an actuation Step must pass its declared GroupBy to the Run, or `groupBy:` is a " +
			"field an estate can declare and nothing will ever read — which is worse than not " +
			"offering it (ADR-0161 D1)")
	}
}
