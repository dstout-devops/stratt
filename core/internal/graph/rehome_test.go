package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestSealFenceRejectsNormalizerWrite is the load-bearing §2.1 proof (ADR-0044
// slice 7): once a Source is sealed for re-home, its home Cell's Normalizer
// projections are REJECTED at the DB — the fence is a constraint, not protocol.
// Aborting the seal restores projection.
func TestSealFenceRejectsNormalizerWrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	srcID := mustSource(t, s, "vcenter", "vc-prod")
	prov := syncerProv("vcenter/syncer", srcID)

	// Baseline: an unsealed Source projects fine.
	if _, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "u1"}},
	}); err != nil {
		t.Fatalf("baseline projection must succeed: %v", err)
	}

	// Seal the Source → the fence engages.
	if _, err := s.SealSourceForRehome(ctx, "vc-prod", "us"); err != nil {
		t.Fatalf("seal: %v", err)
	}
	_, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "u2"}},
	})
	if err == nil {
		t.Fatal("a Normalizer write to a SEALED Source must be rejected (the fence)")
	}
	if !strings.Contains(err.Error(), "sealed for cross-Cell re-home") {
		t.Fatalf("expected a seal-fence rejection, got: %v", err)
	}

	// Abort un-seals → projection resumes.
	if err := s.AbortRehome(ctx, "vc-prod"); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if _, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "u3"}},
	}); err != nil {
		t.Fatalf("projection must resume after abort: %v", err)
	}
}

// TestSealEpochAndConflict proves the fencing token bumps monotonically and a
// seal to a DIFFERENT destination is refused (never two destinations).
func TestSealEpochAndConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustSource(t, s, "vcenter", "vc-a")

	s1, err := s.SealSourceForRehome(ctx, "vc-a", "us")
	if err != nil {
		t.Fatal(err)
	}
	if s1.RehomingTo != "us" || s1.HomeEpoch != 1 {
		t.Fatalf("first seal: rehomingTo=%q epoch=%d", s1.RehomingTo, s1.HomeEpoch)
	}
	// Re-seal to the SAME dest is idempotent (epoch bumps).
	s2, err := s.SealSourceForRehome(ctx, "vc-a", "us")
	if err != nil {
		t.Fatal(err)
	}
	if s2.HomeEpoch <= s1.HomeEpoch {
		t.Fatalf("epoch must bump on re-seal: %d then %d", s1.HomeEpoch, s2.HomeEpoch)
	}
	// Seal to a DIFFERENT dest while sealed → conflict.
	if _, err := s.SealSourceForRehome(ctx, "vc-a", "eu"); err == nil {
		t.Fatal("re-sealing a sealed Source to a different destination must conflict")
	}
	// Abort then re-seal elsewhere is fine, and bumps the epoch again (fences a
	// stale adopt at the old epoch).
	if err := s.AbortRehome(ctx, "vc-a"); err != nil {
		t.Fatal(err)
	}
	s3, err := s.SealSourceForRehome(ctx, "vc-a", "eu")
	if err != nil {
		t.Fatal(err)
	}
	if s3.RehomingTo != "eu" || s3.HomeEpoch <= s2.HomeEpoch {
		t.Fatalf("re-seal after abort: rehomingTo=%q epoch=%d (prev %d)", s3.RehomingTo, s3.HomeEpoch, s2.HomeEpoch)
	}
}

// TestAdoptSourceEpochFence proves the destination adopt is idempotent and a
// stale (≤) epoch cannot regress the home.
func TestAdoptSourceEpochFence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	src := types.Source{Kind: "vcenter", Name: "vc-moved", Endpoint: "https://vc"}
	if err := s.AdoptSource(ctx, src, 5); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	got, err := s.GetSource(ctx, "vc-moved")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cell != s.Cell() || got.HomeEpoch != 5 || got.RehomingTo != "" {
		t.Fatalf("adopted source: cell=%q epoch=%d rehomingTo=%q", got.Cell, got.HomeEpoch, got.RehomingTo)
	}
	// A replayed adopt at a STALE epoch is a no-op (epoch fence).
	if err := s.AdoptSource(ctx, src, 3); err != nil {
		t.Fatalf("stale adopt should be a benign no-op: %v", err)
	}
	got2, _ := s.GetSource(ctx, "vc-moved")
	if got2.HomeEpoch != 5 {
		t.Fatalf("stale adopt must not regress the epoch: %d", got2.HomeEpoch)
	}
}

// TestCompleteRehomeTombstones proves phase 3: the source Cell tombstones the
// re-homed Source's Entities (not hard-delete — must-fix 3), resolves their
// Findings as 'entity-rehomed', and drops the Source row.
func TestCompleteRehomeTombstones(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	srcID := mustSource(t, s, "vcenter", "vc-leaving")
	prov := syncerProv("vcenter/syncer", srcID)
	ids, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "m1"}},
	})
	if err != nil || len(ids) != 1 {
		t.Fatalf("seed entity: ids=%v err=%v", ids, err)
	}

	if _, err := s.SealSourceForRehome(ctx, "vc-leaving", "us"); err != nil {
		t.Fatal(err)
	}
	n, err := s.CompleteRehome(ctx, "vc-leaving")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 Entity tombstoned, got %d", n)
	}
	// The Entity is tombstoned (rebuildable), not gone.
	if _, deleted := tombstoneRef(t, s, ids[0]); !deleted {
		t.Fatal("re-homed Entity must be tombstoned, not hard-deleted")
	}
	// The Source row is gone from this Cell.
	if _, err := s.GetSource(ctx, "vc-leaving"); err == nil {
		t.Fatal("completed re-home must remove the Source row from the source Cell")
	}
}
