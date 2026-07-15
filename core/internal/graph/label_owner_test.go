package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestLabelOwnershipMerge proves per-key Entity-label ownership (ADR-0038): two
// Sources correlating onto one Entity MERGE their (disjoint, owned) labels
// instead of clobbering; a no-label writer does not wipe; and a Syncer cannot
// write a label key it does not own (§2.1/§2.4).
func TestLabelOwnershipMerge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	reg := func(key, ref string) {
		if err := s.RegisterLabelOwner(ctx, types.LabelOwner{Key: key, OwnerKind: "syncer", OwnerRef: ref}); err != nil {
			t.Fatal(err)
		}
	}
	reg("aws.name", "aws/syncer")
	reg("vcenter.name", "vcenter/syncer")

	pa := prov(types.WriterSyncer, "aws/syncer")
	pv := prov(types.WriterSyncer, "vcenter/syncer")

	// Source A writes aws.name; Source B correlates on dns.fqdn and writes
	// vcenter.name — a MERGE, not a whole-bag clobber.
	ids, err := p.UpsertEntities(ctx, pa, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "h.example"}, Labels: map[string]string{"aws.name": "a1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpsertEntities(ctx, pv, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "h.example"}, Labels: map[string]string{"vcenter.name": "v1"}},
	}); err != nil {
		t.Fatal(err)
	}
	e, err := s.GetEntity(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if e.Labels["aws.name"] != "a1" || e.Labels["vcenter.name"] != "v1" {
		t.Fatalf("both Sources' labels must survive the cross-source merge, got %v", e.Labels)
	}

	// A no-label writer (chef-style) must NOT wipe the merged labels.
	if _, err := p.UpsertEntities(ctx, prov(types.WriterSyncer, "chef/syncer"), []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "h.example"}},
	}); err != nil {
		t.Fatal(err)
	}
	e2, _ := s.GetEntity(ctx, ids[0])
	if e2.Labels["aws.name"] != "a1" || e2.Labels["vcenter.name"] != "v1" {
		t.Fatalf("a no-label writer must not wipe labels, got %v", e2.Labels)
	}

	// Ownership enforced: Source A may not write vcenter.name (owned by B).
	if _, err := p.UpsertEntities(ctx, pa, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "h.example"}, Labels: map[string]string{"vcenter.name": "stomp"}},
	}); err == nil {
		t.Fatal("a syncer writing a label key it does not own must be rejected (§2.4)")
	}
	// An unregistered key is rejected too (§2.1: registration precedes writes).
	if _, err := p.UpsertEntities(ctx, pa, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "h2.example"}, Labels: map[string]string{"random": "x"}},
	}); err == nil {
		t.Fatal("an unregistered label key must be rejected (§2.1)")
	}
}

// TestRegisterLabelOwner proves the registry is idempotent for the same owner
// and refuses a different owner (mirrors facet_owner).
func TestRegisterLabelOwner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	o := types.LabelOwner{Key: "aws.name", OwnerKind: "syncer", OwnerRef: "aws/syncer"}
	if err := s.RegisterLabelOwner(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterLabelOwner(ctx, o); err != nil {
		t.Fatalf("same-owner re-registration must be idempotent: %v", err)
	}
	other := types.LabelOwner{Key: "aws.name", OwnerKind: "syncer", OwnerRef: "other/syncer"}
	if err := s.RegisterLabelOwner(ctx, other); !errors.Is(err, ErrOwnerConflict) {
		t.Fatalf("a different owner must be ErrOwnerConflict, got %v", err)
	}
	got, ok, err := s.GetLabelOwner(ctx, "aws.name")
	if err != nil || !ok || got.OwnerRef != "aws/syncer" {
		t.Fatalf("GetLabelOwner: %+v ok=%v err=%v", got, ok, err)
	}
}
