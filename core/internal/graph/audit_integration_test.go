package graph

import (
	"context"
	"fmt"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func appendAudit(t *testing.T, s *Store, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := s.RecordAudit(ctx, types.AuditEvent{
			PrincipalID: "alice", PrincipalKind: "human",
			Action: types.AuditRunStart, Object: fmt.Sprintf("view:v%d", i), Outcome: types.AuditOK,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func rawExec(t *testing.T, s *Store, sql string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// TestLatestAuditForObject proves the recertification "last attested" query
// (ADR-0036): the newest event for a given (action, object) is returned, and a
// never-seen object reports not-found.
func TestLatestAuditForObject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, who := range []string{"alice", "bob"} {
		if err := s.RecordAudit(ctx, types.AuditEvent{
			PrincipalID: who, PrincipalKind: "human",
			Action: "access.recertify", Object: "view:web-hosts", Outcome: types.AuditOK,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A different object must not shadow the query.
	_ = s.RecordAudit(ctx, types.AuditEvent{PrincipalID: "carol", PrincipalKind: "human", Action: "access.recertify", Object: "view:db-hosts", Outcome: types.AuditOK})

	e, ok, err := s.LatestAuditForObject(ctx, "access.recertify", "view:web-hosts")
	if err != nil || !ok {
		t.Fatalf("latest audit: ok=%v err=%v", ok, err)
	}
	if e.PrincipalID != "bob" {
		t.Fatalf("latest attestation must be bob's (newest), got %q", e.PrincipalID)
	}
	if _, ok, _ := s.LatestAuditForObject(ctx, "access.recertify", "view:never"); ok {
		t.Fatal("never-attested object must report not-found")
	}
}

// TestAuditChainSealAndVerify proves the tamper-evidence lifecycle (ADR-0034):
// append is unsealed, the sealer chains the tail, verify passes on a clean
// chain and fails precisely when a sealed row's content is altered.
func TestAuditChainSealAndVerify(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	appendAudit(t, s, 5)

	// Before sealing, nothing is chained: verify sees an empty sealed prefix.
	if v, err := s.VerifyAudit(ctx); err != nil || !v.OK || v.Events != 0 {
		t.Fatalf("pre-seal verify: %+v err=%v", v, err)
	}

	n, err := s.SealPending(ctx)
	if err != nil || n != 5 {
		t.Fatalf("seal: n=%d err=%v", n, err)
	}
	// Idempotent: a second pass seals nothing.
	if n2, _ := s.SealPending(ctx); n2 != 0 {
		t.Fatalf("second seal should be a no-op, sealed %d", n2)
	}
	v, err := s.VerifyAudit(ctx)
	if err != nil || !v.OK || v.Events != 5 || v.SealedThrough != 5 {
		t.Fatalf("clean verify: %+v err=%v", v, err)
	}

	// New events stay unsealed until the next pass; verify still passes over the
	// sealed prefix (the tail is not yet part of the chain).
	appendAudit(t, s, 2)
	if v, _ := s.VerifyAudit(ctx); !v.OK || v.Events != 5 {
		t.Fatalf("verify over sealed prefix: %+v", v)
	}
	if n, _ := s.SealPending(ctx); n != 2 {
		t.Fatalf("seal tail: %d", n)
	}
	if v, _ := s.VerifyAudit(ctx); !v.OK || v.Events != 7 {
		t.Fatalf("verify after sealing tail: %+v", v)
	}

	// Tamper: an attacker with DB write rights disables the append-only trigger
	// and rewrites a sealed row. The hash chain still catches it, at that seq.
	rawExec(t, s, "ALTER TABLE audit.event DISABLE TRIGGER event_immutable")
	rawExec(t, s, "UPDATE audit.event SET outcome='tampered' WHERE seq=3")
	rawExec(t, s, "ALTER TABLE audit.event ENABLE TRIGGER event_immutable")

	bad, err := s.VerifyAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bad.OK || bad.FirstBadSeq != 3 {
		t.Fatalf("verify must fail at seq 3: %+v", bad)
	}
}

// TestAuditTruncationDetected proves a deleted tail is caught: the sealed rows
// no longer reach the seal head (ADR-0034, §1.8).
func TestAuditTruncationDetected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	appendAudit(t, s, 4)
	if _, err := s.SealPending(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.VerifyAudit(ctx); !v.OK {
		t.Fatalf("clean: %+v", v)
	}

	// Delete the last sealed event (bypassing the trigger). seal_head still
	// points past the surviving rows → truncation.
	rawExec(t, s, "ALTER TABLE audit.event DISABLE TRIGGER event_immutable")
	rawExec(t, s, "DELETE FROM audit.event WHERE seq = 4")
	rawExec(t, s, "ALTER TABLE audit.event ENABLE TRIGGER event_immutable")

	bad, err := s.VerifyAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bad.OK {
		t.Fatalf("truncation must be detected: %+v", bad)
	}
}

// TestAuditImmutableTrigger proves the structural append-only guarantee: with
// the trigger in place, a sealed row cannot be updated or deleted at all.
func TestAuditImmutableTrigger(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	appendAudit(t, s, 1)
	if _, err := s.SealPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE audit.event SET outcome='x' WHERE seq=1"); err == nil {
		t.Fatal("updating a sealed audit row must be refused")
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM audit.event WHERE seq=1"); err == nil {
		t.Fatal("deleting an audit row must be refused")
	}
}
