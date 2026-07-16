package graph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// syncerProv builds Syncer provenance stamped with a real registered Source, so
// the entity_presence FK resolves (ADR-0042).
func syncerProv(ref, sourceID string) types.Provenance {
	return types.Provenance{WriterKind: types.WriterSyncer, WriterRef: ref, SourceID: sourceID, At: time.Now().UTC()}
}

func presenceCount(t *testing.T, s *Store, entityID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM graph.entity_presence WHERE entity_id = $1`, entityID).Scan(&n); err != nil {
		t.Fatalf("count presence: %v", err)
	}
	return n
}

// tombstoneRef returns the deleted Entity's restamped writer ref (and whether it
// is tombstoned).
func tombstoneRef(t *testing.T, s *Store, entityID string) (string, bool) {
	t.Helper()
	var ref string
	var deleted *time.Time
	if err := s.pool.QueryRow(context.Background(),
		`SELECT prov_writer_ref, deleted_at FROM graph.entity WHERE id = $1`, entityID).Scan(&ref, &deleted); err != nil {
		t.Fatalf("read entity: %v", err)
	}
	return ref, deleted != nil
}

func mustSource(t *testing.T, s *Store, kind, name string) string {
	t.Helper()
	src, err := s.RegisterSource(context.Background(), types.Source{Kind: kind, Name: name, Endpoint: "x"})
	if err != nil {
		t.Fatalf("register source %s: %v", name, err)
	}
	return src.ID
}

// TestCrossSourceLiveness proves the ADR-0042 union-liveness invariant: a host
// co-managed by two Sources (correlated via dns.fqdn) stays live while EITHER
// Source observes it, is tombstoned only when the LAST Source drops it, and is
// resurrected on re-observation. T7 in the plan (the CTE-regression guard) is
// the "stays live when one Source drops" assertion below.
func TestCrossSourceLiveness(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	chef := syncerProv("chef/syncer", mustSource(t, s, "chef", "acme-chef"))
	puppet := syncerProv("puppet/syncer", mustSource(t, s, "puppet", "acme-puppet"))

	// Chef observes the host; Puppet correlates onto it via the shared dns.fqdn.
	ids, err := p.UpsertEntities(ctx, chef, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1", "dns.fqdn": "f1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if _, err := p.UpsertEntities(ctx, puppet, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"puppet.certname": "h1", "dns.fqdn": "f1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := presenceCount(t, s, eid); got != 2 {
		t.Fatalf("co-managed host must carry 2 presence rows, got %d", got)
	}

	// T1/T7 — Chef stops reporting it: retracts Chef presence, but Puppet still
	// vouches → NOT tombstoned. (A naive single-CTE reconcile tombstones here.)
	n, err := p.TombstoneAbsent(ctx, chef, "chef.node.name", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("co-managed host must not be tombstoned when one Source drops it, got %d tombstoned", n)
	}
	if _, err := s.GetEntity(ctx, eid); err != nil {
		t.Fatalf("host must stay live (Puppet still observes it): %v", err)
	}
	if got := presenceCount(t, s, eid); got != 1 {
		t.Fatalf("after Chef drop, exactly Puppet presence remains, got %d rows", got)
	}

	// T2/T10 — Puppet also stops: last presence retracted → tombstoned, restamped
	// with the retracting Syncer's provenance.
	n, err = p.TombstoneAbsent(ctx, puppet, "puppet.certname", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("last-Source drop must tombstone exactly 1 Entity, got %d", n)
	}
	if _, err := s.GetEntity(ctx, eid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("host must be tombstoned once no Source observes it, got %v", err)
	}
	if got := presenceCount(t, s, eid); got != 0 {
		t.Fatalf("tombstoned host must have zero presence, got %d", got)
	}
	if ref, deleted := tombstoneRef(t, s, eid); !deleted || ref != "puppet/syncer" {
		t.Fatalf("tombstone must restamp the retracting Syncer, got ref=%q deleted=%v", ref, deleted)
	}

	// T3 — resurrection: Chef re-observes the host.
	if _, err := p.UpsertEntities(ctx, chef, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1", "dns.fqdn": "f1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntity(ctx, eid); err != nil {
		t.Fatalf("re-observed host must be live again: %v", err)
	}
	if got := presenceCount(t, s, eid); got != 1 {
		t.Fatalf("resurrected host carries exactly the re-observing Source's presence, got %d", got)
	}
}

// TestRunOnlyEntityNeverTombstoned proves run-provenance Entities stay outside
// the presence system (no rows) and are never tombstoned by a Syncer's absence
// sweep — preserving today's behavior (T4).
func TestRunOnlyEntityNeverTombstoned(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	runProv := types.Provenance{WriterKind: types.WriterRun, WriterRef: "run-1", At: time.Now().UTC()}
	ids, err := s.RunProjector().UpsertEntities(ctx, runProv, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"stratt.vm": "v1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if got := presenceCount(t, s, eid); got != 0 {
		t.Fatalf("run-only Entity must record no presence, got %d", got)
	}

	// A Syncer full-sync that reports nothing under an unrelated scheme must not
	// touch the run-only Entity.
	chef := syncerProv("chef/syncer", mustSource(t, s, "chef", "acme-chef"))
	if _, err := s.NormalizerProjector().TombstoneAbsent(ctx, chef, "chef.node.name", []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntity(ctx, eid); err != nil {
		t.Fatalf("run-only Entity must stay live: %v", err)
	}
}

// TestRunPlusSyncerOverlapFlipsMortal locks the decided observation semantic
// (T5): a run-created Entity a Syncer later observes acquires presence and
// becomes mortal — tombstoned when that Syncer drops it.
func TestRunPlusSyncerOverlapFlipsMortal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	runProv := types.Provenance{WriterKind: types.WriterRun, WriterRef: "run-1", At: time.Now().UTC()}
	ids, err := s.RunProjector().UpsertEntities(ctx, runProv, []EntityUpsert{
		{Kind: "instance", IdentityKeys: map[string]string{"aws.instanceId": "i-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]

	aws := syncerProv("aws/syncer", mustSource(t, s, "awsec2", "acme-aws"))
	if _, err := s.NormalizerProjector().UpsertEntities(ctx, aws, []EntityUpsert{
		{Kind: "instance", IdentityKeys: map[string]string{"aws.instanceId": "i-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := presenceCount(t, s, eid); got != 1 {
		t.Fatalf("Syncer observation must add presence to the run-created Entity, got %d", got)
	}
	n, err := s.NormalizerProjector().TombstoneAbsent(ctx, aws, "aws.instanceId", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("Syncer dropping its last presence must tombstone the (now-observed) Entity, got %d", n)
	}
	if _, err := s.GetEntity(ctx, eid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("overlapped Entity must be tombstoned once the Syncer drops it, got %v", err)
	}
}

// TestTombstoneByIdentityUnion proves the delta-leave path is per-Source too
// (T6): a correlated Entity survives one Source's delta-leave and is tombstoned
// only on the last Source's leave.
func TestTombstoneByIdentityUnion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	chef := syncerProv("chef/syncer", mustSource(t, s, "chef", "acme-chef"))
	puppet := syncerProv("puppet/syncer", mustSource(t, s, "puppet", "acme-puppet"))

	ids, err := p.UpsertEntities(ctx, chef, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1", "dns.fqdn": "f1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if _, err := p.UpsertEntities(ctx, puppet, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"puppet.certname": "h1", "dns.fqdn": "f1"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Chef delta-leave: Puppet still observes → not tombstoned.
	if tombstoned, err := p.TombstoneByIdentity(ctx, chef, "chef.node.name", "h1"); err != nil || tombstoned {
		t.Fatalf("chef delta-leave must not tombstone a co-managed host: tombstoned=%v err=%v", tombstoned, err)
	}
	if _, err := s.GetEntity(ctx, eid); err != nil {
		t.Fatalf("host must stay live after one delta-leave: %v", err)
	}
	if got := presenceCount(t, s, eid); got != 1 {
		t.Fatalf("one presence row must remain, got %d", got)
	}

	// Puppet delta-leave: last Source gone → tombstoned.
	if tombstoned, err := p.TombstoneByIdentity(ctx, puppet, "puppet.certname", "h1"); err != nil || !tombstoned {
		t.Fatalf("last delta-leave must tombstone: tombstoned=%v err=%v", tombstoned, err)
	}
	if _, err := s.GetEntity(ctx, eid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("host must be tombstoned after the last delta-leave, got %v", err)
	}
}

// TestPresenceWritePathGate proves entity_presence writes are gated by §1.2:
// a direct insert outside a Projector transaction is rejected (T8).
func TestPresenceWritePathGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.pool.Exec(ctx,
		`INSERT INTO graph.entity_presence (entity_id, source_id) VALUES (gen_random_uuid(), $1::uuid)`,
		testSourceID)
	if err == nil {
		t.Fatal("direct presence write outside the normalizer path must be rejected (§1.2)")
	}
	if !strings.Contains(err.Error(), "may write the graph projection") {
		t.Fatalf("expected the §1.2 write-path rejection, got %v", err)
	}
}
