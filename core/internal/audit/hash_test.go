package audit

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

func sample() types.AuditEvent {
	return types.AuditEvent{
		Seq: 7, At: time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		PrincipalID: "alice", PrincipalKind: "human",
		Action: "run.start", Object: "view:prod", Outcome: "ok",
		Detail: json.RawMessage(`{"runId":"r1"}`),
	}
}

func TestCanonicalDeterministic(t *testing.T) {
	a, b := Canonical(sample()), Canonical(sample())
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical not deterministic:\n%s\n%s", a, b)
	}
	// It excludes the chain links (prev_hash/hash) — a re-seal of the same
	// content must hash the same regardless of chain position.
	e := sample()
	e.PrevHash, e.Hash = []byte{1, 2}, []byte{3, 4}
	if !bytes.Equal(Canonical(e), a) {
		t.Fatal("canonical must exclude prev_hash/hash")
	}
}

func TestCanonicalSensitiveToContent(t *testing.T) {
	base := Canonical(sample())
	for _, mut := range []func(*types.AuditEvent){
		func(e *types.AuditEvent) { e.Seq = 8 },
		func(e *types.AuditEvent) { e.PrincipalID = "mallory" },
		func(e *types.AuditEvent) { e.Action = "run.cancel" },
		func(e *types.AuditEvent) { e.Object = "view:dev" },
		func(e *types.AuditEvent) { e.Outcome = "denied" },
		func(e *types.AuditEvent) { e.Detail = json.RawMessage(`{"runId":"r2"}`) },
		func(e *types.AuditEvent) { e.At = e.At.Add(time.Second) },
	} {
		e := sample()
		mut(&e)
		if bytes.Equal(Canonical(e), base) {
			t.Fatalf("canonical must change when content changes: %+v", e)
		}
	}
}

func TestChainHashLinks(t *testing.T) {
	// A different prev_hash yields a different link even for identical content —
	// that is what makes altering an earlier event break every later hash.
	e := sample()
	h1 := ChainHash([]byte("genesis-a"), e)
	h2 := ChainHash([]byte("genesis-b"), e)
	if bytes.Equal(h1, h2) {
		t.Fatal("chain hash must depend on prev_hash")
	}
	if len(h1) != 32 {
		t.Fatalf("expected sha256, got %d bytes", len(h1))
	}
	if !bytes.Equal(h1, ChainHash([]byte("genesis-a"), e)) {
		t.Fatal("chain hash must be deterministic")
	}
}
