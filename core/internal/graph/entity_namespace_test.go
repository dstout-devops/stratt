package graph

import (
	"strings"
	"testing"
)

// nest is the tree-builder behind {{.entity.*}} (ADR-0150 D2). Every collision is REFUSED naming
// both namespaces — it used to overwrite in GetFacets order, which is a merge rule decided by map
// iteration, the precise shape §2.4 forbids. It mattered because certificate subjects are bound out
// of this tree: a silently-shadowed value is a certificate issued for the wrong subject, by a route
// nobody can see.
func TestNestRefusesPrefixCollisions(t *testing.T) {
	// A namespace that is a PREFIX of one already placed.
	root, owner := map[string]any{}, map[string]string{}
	if err := nest(root, owner, "cert.presented.notAfter", []string{"cert", "presented", "notAfter"}, "x"); err != nil {
		t.Fatalf("first namespace must place: %v", err)
	}
	err := nest(root, owner, "cert.presented", []string{"cert", "presented"}, map[string]any{"notAfter": "y"})
	if err == nil {
		t.Fatal("a namespace colliding with a deeper one must be refused, not silently overwrite it")
	}
	for _, want := range []string{"cert.presented"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name the colliding namespace (%q missing): %v", want, err)
		}
	}

	// …and the same collision discovered in the OTHER order, because GetFacets order must not
	// decide whether this is an error.
	root2, owner2 := map[string]any{}, map[string]string{}
	if err := nest(root2, owner2, "cert.presented", []string{"cert", "presented"}, map[string]any{"notAfter": "y"}); err != nil {
		t.Fatalf("first namespace must place: %v", err)
	}
	err = nest(root2, owner2, "cert.presented.notAfter", []string{"cert", "presented", "notAfter"}, "x")
	if err == nil {
		t.Fatal("the collision must be refused in EITHER order — iteration order must not decide")
	}
	if !strings.Contains(err.Error(), "cert.presented") || !strings.Contains(err.Error(), "cert.presented.notAfter") {
		t.Fatalf("the refusal must name BOTH namespaces: %v", err)
	}
}

// Sibling namespaces that merely share a prefix are fine — the guard is about one namespace being a
// prefix of another, not about them living in the same subtree.
func TestNestAllowsSiblings(t *testing.T) {
	root, owner := map[string]any{}, map[string]string{}
	if err := nest(root, owner, "mgmt.address", []string{"mgmt", "address"}, map[string]any{"address": "10.0.0.1"}); err != nil {
		t.Fatalf("mgmt.address: %v", err)
	}
	if err := nest(root, owner, "mgmt.site", []string{"mgmt", "site"}, "local"); err != nil {
		t.Fatalf("a sibling namespace must place beside it: %v", err)
	}
	mgmt, ok := root["mgmt"].(map[string]any)
	if !ok || mgmt["site"] != "local" {
		t.Fatalf("both siblings must survive: %+v", root)
	}
}

// The Entity's own coordinates are RESERVED. They used to be written after the facets, so a facet
// namespace called `id` was silently clobbered — {{.entity.id}} then meant different things on
// different Entities.
func TestReservedEntityKeys(t *testing.T) {
	for _, k := range []string{"id", "kind", "identity"} {
		if !reservedEntityKeys[k] {
			t.Fatalf("%q must be reserved so a facet namespace cannot shadow the Entity's own coordinate", k)
		}
	}
	// `labels` is deliberately NOT here, because it is deliberately not exposed at all: a label is
	// a free-form View selector, not a provenance-stamped fact, and a certificate subject derived
	// from one is a far softer claim than one derived from a Facet with a registered write-owner.
	if reservedEntityKeys["labels"] {
		t.Fatal("labels is not exposed in the entity namespace, so it has nothing to reserve")
	}
}
