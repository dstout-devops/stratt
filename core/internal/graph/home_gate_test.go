package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestHomeGateRejectsPeerHomedProjection is the ADR-0045 must-fix-1 proof: the
// destination-side single-writer guarantee is a DB CONSTRAINT. A named daemon's
// Normalizer projection of a Source homed on a NAMED PEER Cell is rejected at the
// data layer — a standby Connector cannot steal a peer's Source.
func TestHomeGateRejectsPeerHomedProjection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.SetCell("eu") // this daemon is Cell eu

	srcID := mustSource(t, s, "vcenter", "vc-peer")
	// Simulate a standby row: the Source is homed on peer Cell "us" (what the
	// deferred resolver would record; today only a re-home produces this).
	if _, err := s.pool.Exec(ctx, `UPDATE graph.source SET cell = 'us' WHERE id = $1`, srcID); err != nil {
		t.Fatal(err)
	}
	_, err := s.NormalizerProjector().UpsertEntities(ctx, syncerProv("vcenter/syncer", srcID),
		[]EntityUpsert{{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "u1"}}})
	if err == nil {
		t.Fatal("a named daemon must NOT project a peer-homed Source (home gate)")
	}
	if !strings.Contains(err.Error(), "homed on peer cell us") {
		t.Fatalf("expected a home-gate rejection, got: %v", err)
	}
}

// TestHomeGateAllowsUnclaimedAndOwnSource proves the gate is precise: a named
// daemon freely projects a Source it homes, and an unclaimed ('local') Source is
// claim-by-projection (legacy + single-Cell byte-identical) — never a false reject.
func TestHomeGateAllowsUnclaimedAndOwnSource(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.SetCell("eu")

	// Own Source (RegisterSource stamped cell=eu): projects fine.
	own := mustSource(t, s, "vcenter", "vc-own")
	if _, err := s.NormalizerProjector().UpsertEntities(ctx, syncerProv("vcenter/syncer", own),
		[]EntityUpsert{{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "o1"}}}); err != nil {
		t.Fatalf("a daemon must project its OWN Source: %v", err)
	}

	// Unclaimed 'local' Source: a named daemon may still project it (claim by
	// projection) — this is what keeps a 'local' estate byte-identical.
	unclaimed := mustSource(t, s, "vcenter", "vc-unclaimed")
	if _, err := s.pool.Exec(ctx, `UPDATE graph.source SET cell = 'local' WHERE id = $1`, unclaimed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NormalizerProjector().UpsertEntities(ctx, syncerProv("vcenter/syncer", unclaimed),
		[]EntityUpsert{{Kind: "vm", IdentityKeys: map[string]string{"vc.uuid": "u2"}}}); err != nil {
		t.Fatalf("a named daemon must project an unclaimed 'local' Source (claim-by-projection): %v", err)
	}
}

// TestRegisterSourceSealSafe proves ADR-0045 must-fix 3: a Connector restart on a
// SEALED Source leaves the row completely untouched — home, endpoint, and the
// fencing epoch all preserved — so a re-register can never corrupt a fenced move.
func TestRegisterSourceSealSafe(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.RegisterSource(ctx, types.Source{Kind: "vcenter", Name: "vc-seal", Endpoint: "https://orig"}); err != nil {
		t.Fatal(err)
	}
	sealed, err := s.SealSourceForRehome(ctx, "vc-seal", "us")
	if err != nil {
		t.Fatal(err)
	}

	// A Connector restart re-registers with a CHANGED endpoint.
	got, err := s.RegisterSource(ctx, types.Source{Kind: "vcenter", Name: "vc-seal", Endpoint: "https://changed"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RehomingTo != "us" {
		t.Fatalf("the seal must survive a re-register, got rehomingTo=%q", got.RehomingTo)
	}
	if got.Endpoint != "https://orig" {
		t.Fatalf("a re-register must NOT modify a sealed Source (endpoint changed to %q)", got.Endpoint)
	}
	if got.HomeEpoch != sealed.HomeEpoch {
		t.Fatalf("a re-register must NOT reset the fencing epoch: got %d want %d", got.HomeEpoch, sealed.HomeEpoch)
	}
}
