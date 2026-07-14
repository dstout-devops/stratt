// Package audit provides the tamper-evidence primitives for the one audit
// stream (charter §1.6, ADR-0034): a canonical encoding of an audit event and
// the hash-chain link over it, plus the background sealer that chains the
// append-only ledger. The store (core/internal/graph) owns persistence; this
// package owns the hashing so the chain math lives in one place, testable in
// isolation and shared by seal and verify.
package audit

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// Canonical renders the immutable content of an audit event to deterministic
// bytes for hashing. It covers exactly the fields that must not change after
// the fact (seq fixes the position; at/principal/action/object/outcome/detail
// fix the content) and deliberately excludes prev_hash/hash (the chain links
// themselves). Field order is fixed by the struct; detail is hashed as its
// stored jsonb bytes (Postgres round-trips jsonb deterministically).
func Canonical(e types.AuditEvent) []byte {
	b, _ := json.Marshal(struct {
		Seq           int64           `json:"seq"`
		At            string          `json:"at"`
		PrincipalID   string          `json:"principalId"`
		PrincipalKind string          `json:"principalKind"`
		Action        string          `json:"action"`
		Object        string          `json:"object"`
		Outcome       string          `json:"outcome"`
		Detail        json.RawMessage `json:"detail"`
	}{
		Seq:           e.Seq,
		At:            e.At.UTC().Format(time.RFC3339Nano),
		PrincipalID:   e.PrincipalID,
		PrincipalKind: e.PrincipalKind,
		Action:        e.Action,
		Object:        e.Object,
		Outcome:       e.Outcome,
		Detail:        e.Detail,
	})
	return b
}

// ChainHash is the tamper-evidence link: sha256(prev_hash || canonical(event)).
// Chaining each event onto the previous one's hash makes any post-hoc edit,
// reorder, or deletion detectable — altering event N changes its hash, which
// breaks every link after it, and a missing seq is a gap the verifier catches.
func ChainHash(prev []byte, e types.AuditEvent) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(Canonical(e))
	sum := h.Sum(nil)
	return sum[:]
}
