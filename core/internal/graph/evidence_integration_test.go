package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// openOneFinding drifts a target past damping 1 so a single open Finding exists,
// and returns it.
func openOneFinding(t *testing.T, store *Store, target string) types.Finding {
	t.Helper()
	ctx := context.Background()
	b := testBaseline(1)
	out, err := store.RecordBaselineObservations(ctx, b, mustRun(t, store), map[string]BaselineObservation{
		target: {Drifted: true, EntityID: "ent-" + target, Detail: []byte(`[{"task":"x"}]`)},
	})
	if err != nil {
		t.Fatalf("open finding: %v", err)
	}
	if out.Opened != 1 {
		t.Fatalf("want 1 opened, got %+v", out)
	}
	f := liveFinding(t, store, b.Name, target)
	if f == nil {
		t.Fatal("finding not found after open")
	}
	return *f
}

func TestEvidenceManifestWriteOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	f := openOneFinding(t, store, "vm-ev1")

	// No evidence yet → the Finding is on the unsealed work-list.
	unsealed, err := store.ListUnsealedFindings(ctx, f.Baseline)
	if err != nil {
		t.Fatalf("list unsealed: %v", err)
	}
	if len(unsealed) != 1 || unsealed[0].ID != f.ID {
		t.Fatalf("want the open finding unsealed, got %+v", unsealed)
	}

	ev := types.Evidence{
		FindingID: f.ID, Baseline: f.Baseline, Target: f.Target,
		ObjectKey: "evidence/" + f.ID + ".json", SHA256: "abc123", SizeBytes: 42,
		RetainUntil: time.Now().Add(24 * time.Hour),
	}
	if err := store.RecordEvidence(ctx, ev); err != nil {
		t.Fatalf("record evidence: %v", err)
	}

	// Now sealed → off the work-list (idempotent seal).
	unsealed, _ = store.ListUnsealedFindings(ctx, f.Baseline)
	if len(unsealed) != 0 {
		t.Fatalf("sealed finding must leave the work-list, got %+v", unsealed)
	}

	// Manifest is retrievable and linked.
	got, err := store.GetEvidenceByFinding(ctx, f.ID)
	if err != nil {
		t.Fatalf("get evidence: %v", err)
	}
	if got.ObjectKey != ev.ObjectKey || got.SHA256 != "abc123" {
		t.Fatalf("manifest round-trip mismatch: %+v", got)
	}

	// Write-once: a second seal of the same Finding is a conflict, never a dup.
	if err := store.RecordEvidence(ctx, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-seal must conflict (write-once), got %v", err)
	}
}
