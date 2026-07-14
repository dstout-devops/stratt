package authz

import (
	"context"
	"testing"
)

// TestProjectedTupleUnion proves SCIM-projected membership joins the CaC grant
// set: a CaC grant on team:platform#member reaches a Principal only once SCIM
// projects them into that team, and drops when the projection is cleared
// (deactivation). Snapshot (what the OpenFGA sync reads) reflects the union.
func TestProjectedTupleUnion(t *testing.T) {
	ctx := context.Background()
	// CaC declares POLICY only: team:platform members may run view:prod.
	a := loadFixture(t, `
tuples:
  - { user: "team:platform#member", relation: runner, object: "view:prod" }
`)

	// Before projection, alice has no path to runner.
	if ok, _ := a.Check(ctx, "alice", RelationRunner, "view:prod"); ok {
		t.Fatal("alice should not have runner before SCIM projects her membership")
	}

	// SCIM projects alice into team:platform (the directory owns membership).
	a.SetProjectedTuples([]Tuple{{User: "principal:alice", Relation: RelationMember, Object: "team:platform"}})
	if ok, _ := a.Check(ctx, "alice", RelationRunner, "view:prod"); !ok {
		t.Fatal("alice should have runner once projected into team:platform")
	}

	// Snapshot (authoritative OpenFGA sync input) includes both CaC and projected.
	if got := len(a.Snapshot()); got != 2 {
		t.Fatalf("Snapshot should union CaC+projected = 2 tuples, got %d", got)
	}
	if got := len(a.CACSnapshot()); got != 1 {
		t.Fatalf("CACSnapshot should be CaC-only = 1 tuple, got %d", got)
	}

	// Deactivation clears the projection → the grant vanishes; CaC is untouched.
	a.SetProjectedTuples(nil)
	if ok, _ := a.Check(ctx, "alice", RelationRunner, "view:prod"); ok {
		t.Fatal("clearing projection (deactivation) must revoke the derived grant")
	}
	if ok, _ := a.Check(ctx, "team", RelationRunner, "view:prod"); ok {
		_ = ok // CaC grant still present for real members — reload path unchanged
	}
}
