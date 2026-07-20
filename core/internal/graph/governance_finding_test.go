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

// TestResolveClearedFindingsByFramework proves the cutover GC (ADR-0087): opens under a
// framework whose (baseline, target) drops out of the live keep-set are resolved; those
// still in it stay open. Exercises the multi-arg unnest + tuple NOT IN SQL directly.
func TestResolveClearedFindingsByFramework(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d := []byte(`{}`)
	if err := s.WriteGovernanceFinding(ctx, "adopt-cutover", "sched-A", "warning", "ansible-cutover", d); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteGovernanceFinding(ctx, "adopt-cutover", "sched-B", "warning", "ansible-cutover", d); err != nil {
		t.Fatal(err)
	}
	// Keep only sched-A live; sched-B (now disabled/reverted) must resolve.
	n, err := s.ResolveClearedFindingsByFramework(ctx, "ansible-cutover", [][2]string{{"adopt-cutover", "sched-A"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("exactly sched-B should resolve, got %d", n)
	}
	open, _ := s.ListFindings(ctx, "adopt-cutover", "open", 10)
	if len(open) != 1 || open[0].Target != "sched-A" {
		t.Fatalf("sched-A must remain the only open finding, got %+v", open)
	}
	// Empty keep-set resolves the remaining live one too (nothing live anywhere).
	if _, err := s.ResolveClearedFindingsByFramework(ctx, "ansible-cutover", nil); err != nil {
		t.Fatal(err)
	}
	open2, _ := s.ListFindings(ctx, "adopt-cutover", "open", 10)
	if len(open2) != 0 {
		t.Fatalf("empty keep-set must resolve all, got %+v", open2)
	}
}
