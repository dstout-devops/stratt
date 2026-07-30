package graph

import (
	"context"
	"testing"
)

// TestTombstoneAbsentWithEmptySeenSet: a full sync that legitimately sees ZERO entities of a
// scheme must retract that Source's presence for all of them. It is the "everything disappeared"
// case — the whole reason tombstoning exists — and it was the one case that did not work.
//
// A nil Go slice encodes as SQL NULL, and `NOT (value = ANY(NULL))` evaluates to NULL rather than
// TRUE, so the retraction matched nothing and the Source's last host stayed present forever.
//
// Found by deleting a built host out-of-band and watching the estate NOT notice: the pod was gone,
// the Syncer's full sync correctly reported zero, and the Entity — and therefore the provisioning
// reconcile's belief that the instance was built — survived it. The declaration still asked for the
// host and nothing re-raised the build, which is config-as-code broken at its most basic point:
// delete the thing behind the estate's back and the estate must notice.
func TestTombstoneAbsentWithEmptySeenSet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	prov := syncerProv("kubecompute/syncer", mustSource(t, s, "kubecompute", "kubecompute"))
	ids, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "stratt-hosts/web-02"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := presenceCount(t, s, ids[0]); got != 1 {
		t.Fatalf("the observed host must carry a presence row, got %d", got)
	}

	// The full sync now sees NOTHING — the fleet dropped to zero. A nil seen-set is exactly what
	// the host passes when no entity of the scheme was observed.
	n, err := p.TombstoneAbsent(ctx, prov, "kube.host", nil)
	if err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if n != 1 {
		t.Fatalf("an empty full sync must tombstone the vanished host, got %d — a nil seen-set "+
			"becomes SQL NULL and NOT(x = ANY(NULL)) is NULL, so nothing matched", n)
	}
	if got := presenceCount(t, s, ids[0]); got != 0 {
		t.Fatalf("presence must be retracted, got %d", got)
	}
}

// The non-empty path is unchanged: a host still reported survives, one dropped is retracted.
func TestTombstoneAbsentKeepsWhatItStillSees(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	prov := syncerProv("kubecompute/syncer", mustSource(t, s, "kubecompute", "kubecompute2"))
	ids, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "ns/keep"}},
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "ns/drop"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.TombstoneAbsent(ctx, prov, "kube.host", []string{"ns/keep"}); err != nil {
		t.Fatal(err)
	}
	if got := presenceCount(t, s, ids[0]); got != 1 {
		t.Fatalf("a host still reported must stay present, got %d", got)
	}
	if got := presenceCount(t, s, ids[1]); got != 0 {
		t.Fatalf("a host no longer reported must be retracted, got %d", got)
	}
}
