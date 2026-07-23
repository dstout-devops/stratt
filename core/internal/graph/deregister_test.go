package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestDeregisterOwnership proves the ADR-0103 Connector-disable primitives: a facet/label
// ownership grant round-trips (register → deregister → re-register), a deregister keyed on
// a DIFFERENT owner_ref is a no-op (never revokes another source's claim), and repeat
// deregisters are idempotent. This is the teardown side of the additive ownership registry.
func TestDeregisterOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	ownerOf := func(ns string) string {
		o, ok, err := s.GetFacetOwner(ctx, ns)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return o.OwnerRef
	}

	// --- Facet owner round-trip ---
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "test.deregister", OwnerKind: "syncer", OwnerRef: "plugin/one/src/syncer"}); err != nil {
		t.Fatal(err)
	}
	if ownerOf("test.deregister") != "plugin/one/src/syncer" {
		t.Fatal("facet owner must be registered")
	}
	// A mismatched owner_ref must NOT revoke the incumbent (multi-source safety, §2.4).
	if err := s.DeregisterFacetOwner(ctx, "test.deregister", "plugin/other/src/syncer"); err != nil {
		t.Fatal(err)
	}
	if ownerOf("test.deregister") != "plugin/one/src/syncer" {
		t.Fatal("deregister by a different owner_ref must be a no-op")
	}
	// The correct owner_ref releases the claim; repeat is idempotent.
	if err := s.DeregisterFacetOwner(ctx, "test.deregister", "plugin/one/src/syncer"); err != nil {
		t.Fatal(err)
	}
	if ownerOf("test.deregister") != "" {
		t.Fatal("facet owner must be deregistered")
	}
	if err := s.DeregisterFacetOwner(ctx, "test.deregister", "plugin/one/src/syncer"); err != nil {
		t.Fatalf("repeat deregister must be idempotent: %v", err)
	}
	// The freed namespace is re-claimable with no precedence tiebreak (re-enable).
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "test.deregister", OwnerKind: "syncer", OwnerRef: "plugin/one/src/syncer"}); err != nil {
		t.Fatalf("a deregistered namespace must be re-claimable: %v", err)
	}
	_ = s.DeregisterFacetOwner(ctx, "test.deregister", "plugin/one/src/syncer") // cleanup

	// --- Label owner round-trip ---
	if err := s.RegisterLabelOwner(ctx, types.LabelOwner{Key: "test.deregister.key", OwnerKind: "syncer", OwnerRef: "plugin/one/src/syncer"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetLabelOwner(ctx, "test.deregister.key"); !ok {
		t.Fatal("label owner must be registered")
	}
	// Mismatched owner_ref → no-op.
	if err := s.DeregisterLabelOwner(ctx, "test.deregister.key", "plugin/other/src/syncer"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetLabelOwner(ctx, "test.deregister.key"); !ok {
		t.Fatal("label deregister by a different owner_ref must be a no-op")
	}
	// Correct owner releases; re-register works.
	if err := s.DeregisterLabelOwner(ctx, "test.deregister.key", "plugin/one/src/syncer"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetLabelOwner(ctx, "test.deregister.key"); ok {
		t.Fatal("label owner must be deregistered")
	}
	if err := s.RegisterLabelOwner(ctx, types.LabelOwner{Key: "test.deregister.key", OwnerKind: "syncer", OwnerRef: "plugin/one/src/syncer"}); err != nil {
		t.Fatalf("a deregistered label key must be re-claimable: %v", err)
	}
	_ = s.DeregisterLabelOwner(ctx, "test.deregister.key", "plugin/one/src/syncer") // cleanup
}
