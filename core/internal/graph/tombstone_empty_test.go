package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
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

// TestRevivedEntityStartsWithNoFacts: a rebuilt host is a NEW instance that reuses an identity,
// and the dead one's Facets must not survive onto it.
//
// Measured before it was fixed: a built host was deleted and rebuilt, and the graph went on
// claiming `software.package: apache2 2.4.68` about a pod that had never had apache — while
// `app.config` still read as SATISFIED, so no drift Finding was raised and the replacement would
// never have been converged. The stale fact was both a lie and a silencer of the mechanism that
// would have corrected it (§1.2).
func TestRevivedEntityStartsWithNoFacts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	prov := syncerProv("kubecompute/syncer", mustSource(t, s, "kubecompute", "kubecompute-revive"))
	// §2.1: registration precedes writes.
	if err := s.RegisterFacetOwner(context.Background(), types.FacetOwner{
		Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: prov.WriterRef,
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "ns/web-9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if err := p.UpsertFacet(ctx, prov, eid, "os.kernel", []byte(`{"family":"linux"}`)); err != nil {
		t.Fatal(err)
	}
	if n := facetCount(t, s, eid); n != 1 {
		t.Fatalf("the observed host must carry its facet, got %d", n)
	}

	// It disappears — the full sync sees nothing — and is tombstoned.
	if _, err := p.TombstoneAbsent(ctx, prov, "kube.host", nil); err != nil {
		t.Fatal(err)
	}

	// …and is REBUILT under the same identity. A new machine, not the old one resuming.
	if _, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "ns/web-9"}},
	}); err != nil {
		t.Fatal(err)
	}
	if n := facetCount(t, s, eid); n != 0 {
		t.Fatalf("a revived Entity must start with NO facts — the dead instance's state must not be "+
			"asserted about its replacement; got %d", n)
	}

	// …and the REPLACEMENT's own facts must then land. Clearing the row restarts the version at 1
	// while facet_history already holds version 1, so this INSERT collided on facet_history_pkey
	// and failed the whole sync — the rebuilt host never got its reach coordinate back. Versions
	// are a position in an append-only record, and that record outlives the row (migration 00046).
	if err := p.UpsertFacet(ctx, prov, eid, "os.kernel", []byte(`{"family":"linux"}`)); err != nil {
		t.Fatalf("the replacement's own facts must land after a revival: %v", err)
	}
	if n := facetCount(t, s, eid); n != 1 {
		t.Fatalf("the replacement must carry its own facet, got %d", n)
	}
}

// An ordinary re-observation of a LIVE Entity must not lose its facts — the clearing is bound to
// revival, not to every upsert.
func TestLiveEntityKeepsItsFactsOnReobservation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	prov := syncerProv("kubecompute/syncer", mustSource(t, s, "kubecompute", "kubecompute-live"))
	if err := s.RegisterFacetOwner(context.Background(), types.FacetOwner{
		Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: prov.WriterRef,
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "ns/web-8"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertFacet(ctx, prov, ids[0], "os.kernel", []byte(`{"family":"linux"}`)); err != nil {
		t.Fatal(err)
	}
	// Observed again on the next cycle, never having died.
	if _, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"kube.host": "ns/web-8"}},
	}); err != nil {
		t.Fatal(err)
	}
	if n := facetCount(t, s, ids[0]); n != 1 {
		t.Fatalf("a live host's facts must survive re-observation, got %d", n)
	}
}

func facetCount(t *testing.T, s *Store, entityID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM graph.facet WHERE entity_id = $1`, entityID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
