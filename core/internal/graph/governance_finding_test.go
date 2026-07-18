package graph

import (
	"context"
	"testing"
)

// A post-review obligation becomes a TRACKED, closeable, idempotent Finding
// (ADR-0075) — "mandatory review" is a real item, not a discarded struct.
func TestWriteGovernanceFinding(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	detail := []byte(`{"obligation":{"type":"post_review","params":{"by":"security-team"}}}`)

	if err := s.WriteGovernanceFinding(ctx, "governance/post-review", "wr-1/guard", "warning", "governance/post-review", detail); err != nil {
		t.Fatal(err)
	}
	fs, err := s.ListFindings(ctx, "governance/post-review", "open", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Target != "wr-1/guard" || fs[0].Framework != "governance/post-review" || fs[0].Status != "open" {
		t.Fatalf("post-review must be one tracked open finding, got %+v", fs)
	}
	// Idempotent per (baseline, target): a re-fired break-glass does not duplicate.
	if err := s.WriteGovernanceFinding(ctx, "governance/post-review", "wr-1/guard", "warning", "governance/post-review", detail); err != nil {
		t.Fatal(err)
	}
	fs2, _ := s.ListFindings(ctx, "governance/post-review", "open", 10)
	if len(fs2) != 1 {
		t.Fatalf("governance finding must be idempotent, got %d", len(fs2))
	}
}
