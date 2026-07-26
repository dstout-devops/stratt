package pluginhost_test

// End-to-end for everything ADR-0128 through ADR-0132 shipped: the full `ansible.*`
// projection lands in a REAL graph through the REAL host path, and the estate
// declarations committed alongside those ADRs actually resolve against it.
//
// This exists because five ADRs in a row added projection depth and every one of them was
// verified at the PLUGIN layer — "the plugin emits these entities" — while the half that
// an operator meets was not: does the graph hold this estate, and do the shipped Views and
// Baselines select the right rows out of it? A View selector is a claim about the
// projection's shape, and until something resolves one against real data it is a claim
// nothing checks. `awx-prod-templates` in particular selects by TOPOLOGY through a CaC
// path ADR-0132 had to add, so it had never run at all.
//
// Seeding goes through pluginhost rather than the projector directly, on purpose: grant
// gating (which label keys survive, which identity schemes are allowed) is exactly where a
// projection bug hides, and a test that bypasses it would prove the wrong thing.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// repoEstate locates the committed estate/ from this test's own file position, so the
// declarations under assertion are the SHIPPED ones and not a fixture that can drift away
// from them.
func repoEstate(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "estate")
}

func facet(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal facet: %v", err)
	}
	return b
}

// awxEstate is the shape the controller half projects, written out as the host receives
// it: two templates (one labelled prod and failing, one not), a workflow invoking one of
// them, a label set, a credential, an org, a team with a member, and two accounts of which
// one is a superuser.
func awxEstate(t *testing.T) []*pluginv1.ObservedEntity {
	t.Helper()
	ent := func(kind, id string, labels map[string]string, f map[string]any, rels ...*pluginv1.ObservedRelation) *pluginv1.ObservedEntity {
		return &pluginv1.ObservedEntity{
			Kind: kind, IdentityKeys: map[string]string{kind: id},
			Labels: labels, Facets: map[string][]byte{kind: facet(t, f)}, Relations: rels,
		}
	}
	rel := func(typ, scheme, val string) *pluginv1.ObservedRelation {
		return &pluginv1.ObservedRelation{Type: typ, ToScheme: scheme, ToValue: val}
	}
	return []*pluginv1.ObservedEntity{
		ent("ansible.org", "ctrl-a/1", map[string]string{"ansible.name": "Platform"},
			map[string]any{"name": "Platform"}),
		ent("ansible.label", "ctrl-a/70", map[string]string{"ansible.name": "prod", "ansible.org": "Platform"},
			map[string]any{"name": "prod", "org": "Platform"}),
		ent("ansible.label", "ctrl-a/72", map[string]string{"ansible.name": "legacy"},
			map[string]any{"name": "legacy"}),
		ent("ansible.executionenvironment", "ctrl-a/80", map[string]string{"ansible.name": "pinned-ee"},
			map[string]any{"name": "pinned-ee", "image": "quay.io/x@sha256:abc", "digestPinned": true}),
		ent("ansible.executionenvironment", "ctrl-a/81", map[string]string{"ansible.name": "floating-ee"},
			map[string]any{"name": "floating-ee", "image": "quay.io/x:latest", "digestPinned": false}),
		ent("ansible.credential", "ctrl-a/50", map[string]string{"ansible.name": "prod-ssh"},
			map[string]any{"name": "prod-ssh", "kind": "ssh"}),
		ent("ansible.user", "ctrl-a/60", map[string]string{"ansible.name": "admin"},
			map[string]any{"username": "admin", "isSuperuser": true, "isActive": true}),
		ent("ansible.user", "ctrl-a/61", map[string]string{"ansible.name": "ops"},
			map[string]any{"username": "ops", "isSuperuser": false, "isActive": true}),
		ent("ansible.team", "ctrl-a/1", map[string]string{"ansible.name": "web-ops"},
			map[string]any{"name": "web-ops"},
			rel("member-of", "ansible.org", "ctrl-a/1"),
			rel("has-member", "ansible.user", "ctrl-a/61")),
		// The production template: labelled prod, using a credential, and FAILING.
		ent("ansible.template", "ctrl-a/10", map[string]string{"ansible.name": "Deploy Web", "ansible.org": "Platform"},
			map[string]any{"name": "Deploy Web", "lastRunFailed": true, "lastRunStatus": "failed", "limit": "web*"},
			rel("owned-by", "ansible.org", "ctrl-a/1"),
			rel("has-label", "ansible.label", "ctrl-a/70"),
			rel("uses-credential", "ansible.credential", "ctrl-a/50"),
			rel("runs-in", "ansible.executionenvironment", "ctrl-a/80")),
		// The other one: legacy-labelled and green.
		ent("ansible.template", "ctrl-a/11", map[string]string{"ansible.name": "Gather Facts"},
			map[string]any{"name": "Gather Facts", "lastRunFailed": false, "lastRunStatus": "successful"},
			rel("has-label", "ansible.label", "ctrl-a/72"),
			rel("runs-in", "ansible.executionenvironment", "ctrl-a/81")),
		ent("ansible.workflow", "ctrl-a/20", map[string]string{"ansible.name": "prod-pipeline"},
			map[string]any{"name": "prod-pipeline", "nodeCount": 5, "hasApprovalGate": true},
			rel("invokes", "ansible.template", "ctrl-a/10")),
		ent("ansible.schedule", "ctrl-a/30", map[string]string{"ansible.name": "nightly"},
			map[string]any{"name": "nightly", "enabled": true, "timezone": "Europe/London",
				"extraDataKeys": []string{"app_version"}},
			rel("schedules", "ansible.template", "ctrl-a/10")),
	}
}

// projectAWX registers the controller half's grant and syncs the estate above through the
// real host, returning the store.
func projectAWX(t *testing.T) *graph.Store {
	t.Helper()
	store := newTestStore(t)
	ctx := context.Background()
	client := serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(controllerNS), tombstoneSchemes: controllerNS,
		entities: awxEstate(t),
	})
	h := pluginhost.New(store, client, controllerGrant("ansible-controller-ctrl-a"), quietLog())
	if err := h.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return store
}

// shippedView loads a View from the committed estate by name.
func shippedView(t *testing.T, name string) types.ViewSelector {
	t.Helper()
	decls, err := desiredstate.ParseDir(repoEstate(t), nil)
	if err != nil {
		t.Fatalf("parse the committed estate: %v", err)
	}
	for _, v := range decls.Views {
		if v.Name == name {
			return v.Selector
		}
	}
	t.Fatalf("View %q is not in the committed estate", name)
	return types.ViewSelector{}
}

func resolvedNames(t *testing.T, store *graph.Store, sel types.ViewSelector) []string {
	t.Helper()
	ents, err := store.ResolveSelector(context.Background(), sel, nil, 100)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Labels["ansible.name"])
	}
	sort.Strings(out)
	return out
}

// The whole projection lands: every kind ADR-0128..0132 added is in the graph, through
// the grant, with its facets validated at the write path where a schema is pinned.
func TestAWXProjectionLandsEveryKind(t *testing.T) {
	store := projectAWX(t)
	for kind, want := range map[string]int{
		"ansible.template": 2, "ansible.workflow": 1, "ansible.schedule": 1,
		"ansible.org": 1, "ansible.team": 1, "ansible.credential": 1,
		"ansible.user": 2, "ansible.label": 2, "ansible.executionenvironment": 2,
	} {
		if got := kindCount(t, store, kind); got != want {
			t.Errorf("%s projected %d, want %d", kind, got, want)
		}
	}
}

// ADR-0132 D1's payoff, resolved against real rows for the first time: the SHIPPED
// awx-prod-templates View selects by topology — templates carrying a has-label edge to the
// label named `prod` — and must return exactly the production one.
//
// This is the assertion with the most ways to be quietly wrong. It depends on the label
// Entity surviving the grant's label-key allowlist (so `ansible.name` is queryable on the
// TARGET), on the edge resolving same-source, and on the CaC decoder having carried the
// predicate through at all — which before ADR-0132 it did not.
func TestShippedProdTemplatesViewSelectsByTopology(t *testing.T) {
	store := projectAWX(t)
	got := resolvedNames(t, store, shippedView(t, "awx-prod-templates"))
	if len(got) != 1 || got[0] != "Deploy Web" {
		t.Fatalf("awx-prod-templates resolved %v, want exactly [Deploy Web] — the legacy-labelled template must not match", got)
	}
}

// The kind-scoped Views ship as the surfaces their Baselines read.
func TestShippedAWXViewsResolve(t *testing.T) {
	store := projectAWX(t)
	for name, want := range map[string][]string{
		"awx-templates":              {"Deploy Web", "Gather Facts"},
		"awx-users":                  {"admin", "ops"},
		"awx-credentials":            {"prod-ssh"},
		"awx-execution-environments": {"floating-ee", "pinned-ee"},
		"awx-workflows":              {"prod-pipeline"},
	} {
		got := resolvedNames(t, store, shippedView(t, name))
		if len(got) != len(want) {
			t.Errorf("%s resolved %v, want %v", name, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s resolved %v, want %v", name, got, want)
				break
			}
		}
	}
}

// The Baselines' facet predicates must address fields that actually exist in the
// projection. A path typo makes a Baseline silently match nothing — it does not error, it
// just never fires, which is the §1.8 failure a governance surface can least afford.
// Asserted by resolving each Baseline's expectation as a selector over its own View.
func TestShippedAWXBaselinePredicatesAddressRealFields(t *testing.T) {
	store := projectAWX(t)
	decls, err := desiredstate.ParseDir(repoEstate(t), nil)
	if err != nil {
		t.Fatalf("parse the committed estate: %v", err)
	}

	// baseline name -> (the entity that VIOLATES it, by ansible.name)
	violators := map[string]string{
		"awx-template-failing": "Deploy Web", // lastRunFailed == true
		"awx-superuser-review": "admin",      // isSuperuser == true
		"awx-schedule-enabled": "",           // nothing disabled in this estate
	}
	var checked int
	for _, b := range decls.Baselines {
		want, tracked := violators[b.Name]
		if !tracked || len(b.Expected) == 0 {
			continue
		}
		checked++
		sel := shippedView(t, b.ViewName)
		// The Baseline asserts expected==X; the violators are the entities where the
		// field is NOT X. Resolve the negation by selecting on the violating value.
		for _, exp := range b.Expected {
			neg := sel
			var flipped json.RawMessage
			switch string(exp.Equals) {
			case "false":
				flipped = json.RawMessage("true")
			case "true":
				flipped = json.RawMessage("false")
			default:
				t.Fatalf("%s: expectation on %s is not boolean (%s) — this test only models the boolean Baselines", b.Name, exp.Path, exp.Equals)
			}
			neg.Facets = append(append([]types.FacetPredicate{}, sel.Facets...), types.FacetPredicate{
				Namespace: exp.Namespace, Path: exp.Path, Equals: flipped,
			})
			got := resolvedNames(t, store, neg)
			if want == "" {
				if len(got) != 0 {
					t.Errorf("%s: expected no violators in this estate, got %v", b.Name, got)
				}
				continue
			}
			if len(got) != 1 || got[0] != want {
				t.Errorf("%s: facet predicate %s.%s matched %v, want exactly [%s] — a path that addresses nothing makes the Baseline silently never fire (§1.8)",
					b.Name, exp.Namespace, exp.Path, got, want)
			}
		}
	}
	if checked != len(violators) {
		t.Fatalf("checked %d Baselines, expected %d — a tracked Baseline is missing from the committed estate", checked, len(violators))
	}
}
