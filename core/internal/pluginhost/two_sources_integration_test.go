package pluginhost_test

// The ADR-0127 D1/D3 invariants, asserted against a real graph store: ONE plugin
// identity, TWO Grants, TWO Sources. These are core-side properties — they live in
// pluginhost because that is what enforces them, and because core imports no plugin
// module (ADR-0046 isolation). The plugin's own half of the proof — that its two
// manifests are one identity with disjoint namespaces — is
// plugins/ansible-automation/automation_test.go.
//
// Every test here corresponds to a sentence the ADR asserts in prose. Before this file
// those sentences were arguments; the boot blocks that wire the two halves are env-gated
// and set nowhere in-repo, so nothing had ever registered two grants under one identity.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// The nine `ansible.*` namespaces, split exactly as the two halves own them.
var (
	controllerNS = []string{"ansible.template", "ansible.workflow", "ansible.schedule", "ansible.org", "ansible.team", "ansible.credential", "ansible.user", "ansible.label", "ansible.executionenvironment"}
	contentNS    = []string{"ansible.playbook", "ansible.role", "ansible.collection", "ansible.inventory"}
)

const onePluginIdentity = "ansible-automation"

func decls(ns []string) []*pluginv1.ContractDecl {
	out := make([]*pluginv1.ContractDecl, 0, len(ns))
	for _, n := range ns {
		out = append(out, &pluginv1.ContractDecl{SchemaId: n})
	}
	return out
}

// controllerGrant mirrors the strattd boot block: the five owned namespaces, PLUS
// ansible.playbook as a POINTABLE-ONLY IdentityScheme (never a FacetNamespace) so the
// cross-source `runs` edge can name a playbook this Source must never own.
func controllerGrant(name string) pluginhost.Grant {
	return pluginhost.Grant{
		PluginIdentity:   onePluginIdentity,
		Tier:             pluginhost.TierTrusted,
		Source:           types.Source{Kind: "ansible.controller", Name: name, Endpoint: "https://aap.example.com"},
		LabelKeys:        []string{"ansible.name", "ansible.org"},
		FacetNamespaces:  controllerNS,
		IdentitySchemes:  append(append([]string{}, controllerNS...), "ansible.playbook"),
		TombstoneSchemes: controllerNS,
	}
}

func contentGrant(name string) pluginhost.Grant {
	return pluginhost.Grant{
		PluginIdentity:   onePluginIdentity,
		Tier:             pluginhost.TierTrusted,
		Source:           types.Source{Kind: "ansible.content", Name: name},
		LabelKeys:        []string{"ansible.artifact", "ansible.project"},
		FacetNamespaces:  contentNS,
		IdentitySchemes:  contentNS,
		TombstoneSchemes: contentNS,
	}
}

// playbookEnt projects a playbook the way the content half does. The facet must satisfy
// the PINNED ansible.playbook schema (name+path required, closed) — one of only two
// `ansible.*` namespaces that has one.
func playbookEnt(id string) *pluginv1.ObservedEntity {
	return &pluginv1.ObservedEntity{
		Kind:         "ansible.playbook",
		IdentityKeys: map[string]string{"ansible.playbook": id},
		Labels:       map[string]string{"ansible.project": "webproj", "ansible.artifact": "site.yml"},
		Facets:       map[string][]byte{"ansible.playbook": []byte(`{"name":"site.yml","path":"site.yml"}`)},
	}
}

// templateEnt projects a job template, optionally with the cross-source `runs` edge onto
// a playbook identity owned by the OTHER Source.
func templateEnt(id, runsPlaybook string) *pluginv1.ObservedEntity {
	e := &pluginv1.ObservedEntity{
		Kind:         "ansible.template",
		IdentityKeys: map[string]string{"ansible.template": id},
		Labels:       map[string]string{"ansible.name": "Deploy Web"},
		Facets:       map[string][]byte{"ansible.template": []byte(`{"name":"Deploy Web","playbook":"site.yml"}`)},
	}
	if runsPlaybook != "" {
		e.Relations = []*pluginv1.ObservedRelation{{
			Type: "runs", ToScheme: "ansible.playbook", ToValue: runsPlaybook,
		}}
	}
	return e
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func kindCount(t *testing.T, store *graph.Store, kind string) int {
	t.Helper()
	es, err := store.ResolveSelector(context.Background(), types.ViewSelector{Kinds: []string{kind}}, nil, 100)
	if err != nil {
		t.Fatalf("resolve %s: %v", kind, err)
	}
	return len(es)
}

// D1, the core claim: ONE plugin identity, TWO Grants, TWO distinct graph.source rows —
// each owning its own disjoint half of the `ansible.*` namespaces. The identity binding
// in Register checks manifest.plugin_id == grant.PluginIdentity, and BOTH grants carry
// the same identity, which is what makes this one plugin rather than two.
func TestTwoGrantsOneIdentityRegisterTwoSources(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ctrlClient := serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(controllerNS), tombstoneSchemes: controllerNS,
		entities: []*pluginv1.ObservedEntity{templateEnt("ctrl-a/10", "")},
	})
	contClient := serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(contentNS), tombstoneSchemes: contentNS,
		entities: []*pluginv1.ObservedEntity{playbookEnt("webproj/site.yml")},
	})

	ctrlHost := pluginhost.New(store, ctrlClient, controllerGrant("ansible-controller-ctrl-a"), quietLog())
	contHost := pluginhost.New(store, contClient, contentGrant("ansible-content-webproj"), quietLog())

	if err := ctrlHost.Register(ctx); err != nil {
		t.Fatalf("controller half register: %v", err)
	}
	if err := contHost.Register(ctx); err != nil {
		t.Fatalf("content half register: %v", err)
	}

	ctrlSrc, err := store.GetSource(ctx, "ansible-controller-ctrl-a")
	if err != nil {
		t.Fatalf("get controller source: %v", err)
	}
	contSrc, err := store.GetSource(ctx, "ansible-content-webproj")
	if err != nil {
		t.Fatalf("get content source: %v", err)
	}
	if ctrlSrc.ID == contSrc.ID {
		t.Fatal("both halves registered the SAME graph.source — the split that keeps one half's full sync from retracting the other's entities is gone (ADR-0127 D1)")
	}
	if ctrlSrc.Kind != "ansible.controller" || contSrc.Kind != "ansible.content" {
		t.Fatalf("source kinds = %q / %q, want ansible.controller / ansible.content", ctrlSrc.Kind, contSrc.Kind)
	}

	if err := ctrlHost.Sync(ctx); err != nil {
		t.Fatalf("controller sync: %v", err)
	}
	if err := contHost.Sync(ctx); err != nil {
		t.Fatalf("content sync: %v", err)
	}
	if n := kindCount(t, store, "ansible.template"); n != 1 {
		t.Errorf("ansible.template projected %d, want 1", n)
	}
	if n := kindCount(t, store, "ansible.playbook"); n != 1 {
		t.Errorf("ansible.playbook projected %d, want 1", n)
	}
}

// The D1 CORRECTION, asserted: a single Manifest advertising the union of both halves
// registers as NEITHER. This is why one address cannot serve both grants and why the
// deployment unit is one instance per Source. Before the correction the ADR claimed the
// opposite; this test is what makes the claim checkable rather than arguable.
func TestUnionManifestRegistersAsNeitherHalf(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	union := append(append([]string{}, controllerNS...), contentNS...)
	client := serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(union), tombstoneSchemes: union,
	})

	err := pluginhost.New(store, client, controllerGrant("union-ctrl"), quietLog()).Register(ctx)
	if err == nil {
		t.Fatal("a union manifest registered under the CONTROLLER grant — Register must reject the four content namespaces it does not own")
	}
	if !strings.Contains(err.Error(), "ansible.") {
		t.Errorf("controller rejection does not name the offending namespace: %v", err)
	}

	err = pluginhost.New(store, client, contentGrant("union-content"), quietLog()).Register(ctx)
	if err == nil {
		t.Fatal("a union manifest registered under the CONTENT grant — Register must reject the five controller namespaces it does not own")
	}
	if !strings.Contains(err.Error(), "ansible.") {
		t.Errorf("content rejection does not name the offending namespace: %v", err)
	}
}

// The edge the whole split exists to protect (ADR-0085): `ansible.template --runs-->
// ansible.playbook` crosses from the controller Source to the content Source. It
// RESOLVES when the content half has projected the playbook, and DROPS — never
// auto-vivifies a stub — when it has not. That dropped edge IS the orphan-template
// governance signal, so this test asserts both directions.
func TestCrossSourceRunsEdgeResolvesAndOrphanDrops(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// The content half goes first: the playbook must exist for the edge to resolve.
	contClient := serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(contentNS), tombstoneSchemes: contentNS,
		entities: []*pluginv1.ObservedEntity{playbookEnt("webproj/site.yml")},
	})
	contHost := pluginhost.New(store, contClient, contentGrant("ansible-content-webproj"), quietLog())
	if err := contHost.Register(ctx); err != nil {
		t.Fatalf("content register: %v", err)
	}
	if err := contHost.Sync(ctx); err != nil {
		t.Fatalf("content sync: %v", err)
	}

	// One template pointing at a projected playbook, one at a playbook nobody projects.
	ctrlClient := serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(controllerNS), tombstoneSchemes: controllerNS,
		entities: []*pluginv1.ObservedEntity{
			templateEnt("ctrl-a/10", "webproj/site.yml"),
			templateEnt("ctrl-a/11", "webproj/never-projected.yml"),
		},
	})
	ctrlHost := pluginhost.New(store, ctrlClient, controllerGrant("ansible-controller-ctrl-a"), quietLog())
	if err := ctrlHost.Register(ctx); err != nil {
		t.Fatalf("controller register: %v", err)
	}
	if err := ctrlHost.Sync(ctx); err != nil {
		t.Fatalf("controller sync: %v", err)
	}

	// Both templates project regardless — a dropped edge never drops its entity.
	if n := kindCount(t, store, "ansible.template"); n != 2 {
		t.Fatalf("ansible.template projected %d, want 2 (a dropped edge must not drop its entity)", n)
	}
	// And no stub was vivified for the playbook nobody projects (§1.2 / ADR-0047).
	if n := kindCount(t, store, "ansible.playbook"); n != 1 {
		t.Fatalf("ansible.playbook count = %d, want 1 — an unresolved relation target must be DROPPED, never vivified", n)
	}

	// The resolved edge exists and crosses the two Sources.
	pbs, err := store.ResolveSelector(ctx, types.ViewSelector{Kinds: []string{"ansible.playbook"}}, nil, 10)
	if err != nil || len(pbs) != 1 {
		t.Fatalf("resolve playbook: %v (n=%d)", err, len(pbs))
	}
	from, err := store.RelationSources(ctx, pbs[0].ID, "runs")
	if err != nil {
		t.Fatalf("relation sources onto playbook: %v", err)
	}
	if len(from) != 1 {
		t.Fatalf("`runs` edges onto the projected playbook = %d, want exactly 1 (the cross-source edge ADR-0085 rests on)", len(from))
	}
}

// D3, corrected TWICE — once by reading the SQL, once by running this test.
//
// The ADR said changing PluginIdentity/Source.Kind stamps a new graph.source. It does
// not: RegisterSource is ON CONFLICT (name) DO UPDATE SET kind, so only a Source.NAME
// change makes a new row. That was correction one.
//
// Correction two, found here: D3's step 1 says the new Sources "register and observe"
// while the old ones are still registered. That works for FACET namespaces, which are
// multi-owner (ADR-0060), and is REFUSED for LABEL keys, which are single-owner
// (ADR-0041). A renamed Source is the same half with a new WriterRef, so it claims the
// same label keys and Register fails until the old claim is released.
//
// So the migration needs an explicit deregister step — and it is still boring, because
// Host.Deregister releases the OWNERSHIP claims while deliberately leaving the Source row
// and its presence rows alone. Liveness therefore never dips: the entity is observed by
// the old Source throughout, and by both once the new one syncs. This test walks the
// corrected sequence and asserts the entity is live at EVERY step, which is the property
// D3 exists to guarantee.
func TestSourceRenameMigrationIsAdditive(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	live := func(step string) {
		t.Helper()
		if n := kindCount(t, store, "ansible.playbook"); n != 1 {
			t.Fatalf("playbook count = %d at %q, want 1 — an entity must never be momentarily unobserved during the rename (ADR-0042 union liveness; a 0 here is the spurious-orphan window D3 exists to avoid)", n, step)
		}
	}

	oldHost := pluginhost.New(store, serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(contentNS), tombstoneSchemes: contentNS,
		entities: []*pluginv1.ObservedEntity{playbookEnt("webproj/site.yml")},
	}), contentGrant("ansible-project-webproj"), quietLog())
	if err := oldHost.Register(ctx); err != nil {
		t.Fatalf("old-name register: %v", err)
	}
	if err := oldHost.Sync(ctx); err != nil {
		t.Fatalf("old-name sync: %v", err)
	}
	live("old Source observing")

	// The step the ADR omitted. Ownership claims are released; the Source row and every
	// presence row it wrote stay exactly where they are (Host.Deregister is explicit that
	// lifecycle is the home-gate single writer's domain, §2.4).
	if err := oldHost.Deregister(ctx); err != nil {
		t.Fatalf("old-name deregister: %v", err)
	}
	live("old ownership released, before the new Source registers")

	newHost := pluginhost.New(store, serve(t, &fakePlugin{
		pluginID: onePluginIdentity, contracts: decls(contentNS), tombstoneSchemes: contentNS,
		entities: []*pluginv1.ObservedEntity{playbookEnt("webproj/site.yml")},
	}), contentGrant("ansible-content-webproj"), quietLog())
	if err := newHost.Register(ctx); err != nil {
		t.Fatalf("new-name register (the step that fails without the deregister above): %v", err)
	}
	if err := newHost.Sync(ctx); err != nil {
		t.Fatalf("new-name sync: %v", err)
	}
	live("both Sources observing")

	oldSrc, err := store.GetSource(ctx, "ansible-project-webproj")
	if err != nil {
		t.Fatalf("get old source: %v", err)
	}
	newSrc, err := store.GetSource(ctx, "ansible-content-webproj")
	if err != nil {
		t.Fatalf("get new source: %v", err)
	}
	if oldSrc.ID == newSrc.ID {
		t.Fatal("the renamed Source reused the old row — D3's additive migration assumes a NAME change stamps a new graph.source")
	}
	if oldSrc.Kind != newSrc.Kind {
		t.Fatalf("source kinds diverged (%q vs %q) — both are the content half", oldSrc.Kind, newSrc.Kind)
	}
}
