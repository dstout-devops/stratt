package mesh

import (
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// index the normalized output by FQDN for assertions.
func byFQDN(ents []*pluginv1.ObservedEntity) map[string]*pluginv1.ObservedEntity {
	m := map[string]*pluginv1.ObservedEntity{}
	for _, e := range ents {
		m[e.IdentityKeys[SchemeFQDN]] = e
	}
	return m
}

func depTargets(e *pluginv1.ObservedEntity) []string {
	var out []string
	for _, r := range e.GetRelations() {
		if r.GetType() == "depends-on" {
			out = append(out, r.GetToScheme()+"="+r.GetToValue())
		}
	}
	return out
}

// TestNormalize_AnchorsAndEdges: every FQDN (caller OR callee) becomes an identity-only
// `service` anchor so the edge resolves (the host never vivifies a target), and each
// caller carries its `depends-on` edges targeting the callee by the shared dns.fqdn.
func TestNormalize_AnchorsAndEdges(t *testing.T) {
	const (
		web = "web.prod.svc.cluster.local"
		api = "api.prod.svc.cluster.local"
		db  = "db.prod.svc.cluster.local" // only ever a callee — must still be anchored
	)
	out := Normalize([]TrafficEdge{
		{FromFQDN: web, ToFQDN: api},
		{FromFQDN: api, ToFQDN: db},
	})
	m := byFQDN(out)

	if len(out) != 3 {
		t.Fatalf("expected 3 service anchors (web, api, db), got %d", len(out))
	}
	if _, ok := m[db]; !ok {
		t.Fatal("db must be anchored even though it is only ever a callee (no-vivify: else web/api→db would drop)")
	}
	for _, e := range out {
		if e.Kind != "service" {
			t.Fatalf("anchor kind must be `service`, got %q", e.Kind)
		}
		if len(e.Facets) != 0 {
			t.Fatalf("mesh must emit NO facet (§2.1 — kubeservices owns service.endpoint), got %v", e.Facets)
		}
		if len(e.Labels) != 0 {
			t.Fatalf("mesh anchor must be identity-only (no label), got %v", e.Labels)
		}
	}
	if got := depTargets(m[web]); len(got) != 1 || got[0] != SchemeFQDN+"="+api {
		t.Fatalf("web must depend-on api, got %v", got)
	}
	if got := depTargets(m[api]); len(got) != 1 || got[0] != SchemeFQDN+"="+db {
		t.Fatalf("api must depend-on db, got %v", got)
	}
	if got := depTargets(m[db]); len(got) != 0 {
		t.Fatalf("db is a leaf — no outbound dependency, got %v", got)
	}
}

// TestNormalize_DropsSelfAndDedups: a self-edge is not a dependency; duplicate edges
// (a live gauge reports the same pair from many workloads) collapse to one.
func TestNormalize_DropsSelfAndDedups(t *testing.T) {
	const (
		a = "a.prod.svc.cluster.local"
		b = "b.prod.svc.cluster.local"
	)
	out := Normalize([]TrafficEdge{
		{FromFQDN: a, ToFQDN: a}, // self — dropped
		{FromFQDN: a, ToFQDN: b},
		{FromFQDN: a, ToFQDN: b},  // duplicate — collapses
		{FromFQDN: "", ToFQDN: b}, // incomplete — dropped
	})
	m := byFQDN(out)

	// `a` calling itself must not anchor `a` on the strength of the self-edge alone;
	// but the a→b edge legitimately anchors both.
	if len(out) != 2 {
		t.Fatalf("expected anchors {a,b}, got %d: %v", len(out), out)
	}
	if got := depTargets(m[a]); len(got) != 1 || got[0] != SchemeFQDN+"="+b {
		t.Fatalf("a must depend-on b exactly once (self dropped, dup collapsed), got %v", got)
	}
}

// TestNormalize_Deterministic: anchor and edge order is stable regardless of input
// order, so the projection (and its full-sync sweep) is reproducible.
func TestNormalize_Deterministic(t *testing.T) {
	const (
		x = "x.svc"
		y = "y.svc"
		z = "z.svc"
	)
	forward := Normalize([]TrafficEdge{{FromFQDN: x, ToFQDN: y}, {FromFQDN: x, ToFQDN: z}})
	reverse := Normalize([]TrafficEdge{{FromFQDN: x, ToFQDN: z}, {FromFQDN: x, ToFQDN: y}})

	if len(forward) != len(reverse) {
		t.Fatalf("length differs: %d vs %d", len(forward), len(reverse))
	}
	for i := range forward {
		if forward[i].IdentityKeys[SchemeFQDN] != reverse[i].IdentityKeys[SchemeFQDN] {
			t.Fatalf("anchor order not deterministic at %d: %q vs %q", i,
				forward[i].IdentityKeys[SchemeFQDN], reverse[i].IdentityKeys[SchemeFQDN])
		}
	}
	// x's two edges are ordered too.
	fx := byFQDN(forward)[x]
	if got := depTargets(fx); len(got) != 2 || got[0] != SchemeFQDN+"="+y || got[1] != SchemeFQDN+"="+z {
		t.Fatalf("x edges not deterministically ordered: %v", got)
	}
}
