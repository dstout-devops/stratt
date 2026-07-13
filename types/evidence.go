package types

import "time"

// Evidence is the manifest for one Finding's sealed audit bundle (charter §2.4:
// "immutable (object-locked) artifact bundle backing a Finding; the audit/PCI
// export unit"; ADR-0029). The immutable bundle lives in the object store; this
// row is the graph's POINTER to it (a projection, not a second copy — §1.2). The
// SHA256 is the tamper-evidence anchor: a read re-hashes the object and rejects a
// mismatch, so the bundle cannot be silently altered regardless of backend.
type Evidence struct {
	ID string `json:"id"`
	// FindingID is the Finding this bundle backs.
	FindingID string `json:"findingId"`
	Baseline  string `json:"baseline"`
	Target    string `json:"target"`
	// ObjectKey addresses the sealed bundle in the object store.
	ObjectKey string `json:"objectKey"`
	// SHA256 is hex(sha256(bundle)) — the integrity/tamper-evidence anchor.
	SHA256 string `json:"sha256"`
	// SizeBytes is the sealed bundle size.
	SizeBytes int64 `json:"sizeBytes"`
	// SealedAt is when the bundle was written.
	SealedAt time.Time `json:"sealedAt"`
	// RetainUntil is the object-lock retain-until date applied at seal time
	// (enforced as WORM by a compliant object store; see ADR-0029 on the dev
	// backend's non-enforcement).
	RetainUntil time.Time `json:"retainUntil"`
}
