package events

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// TestBusScopeIsolation is the real-substrate proof of slice 6: two Buses on the
// SAME NATS, scoped to different Cells, must not see each other's Run events —
// distinct streams + distinct subjects. NATS-gated (skips when the dev substrate
// is down), so it runs in CI where `task dev:up` is live.
func TestBusScopeIsolation(t *testing.T) {
	url := os.Getenv("STRATT_TEST_NATS_URL")
	if url == "" {
		url = os.Getenv("STRATT_NATS_URL")
	}
	if url == "" {
		url = "nats://localhost:4222"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eu, err := Connect(ctx, url, "eu")
	if err != nil {
		t.Skipf("no test NATS reachable (%v) — run `task dev:up`", err)
	}
	defer eu.Close()
	us, err := Connect(ctx, url, "us")
	if err != nil {
		t.Skipf("no test NATS reachable (%v)", err)
	}
	defer us.Close()

	// Distinct scoped stream names — the coarse isolation.
	if eu.StreamName() == us.StreamName() {
		t.Fatalf("distinct Cells must not share a Run-event stream: %q", eu.StreamName())
	}
	if eu.StreamName() != types.ScopedStream(StreamName, "eu") {
		t.Fatalf("eu stream name drift: %q", eu.StreamName())
	}

	// Publish on eu; a tail on us for the same RunID must see NOTHING.
	if err := eu.Publish(ctx, types.RunEvent{RunID: "run-xcell", Slice: 0, Seq: 1, Kind: "task"}); err != nil {
		t.Fatalf("eu publish: %v", err)
	}
	tctx, tcancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer tcancel()
	seen := 0
	_ = us.Tail(tctx, "run-xcell", func(types.RunEvent) error { seen++; return nil })
	if seen != 0 {
		t.Fatalf("us Cell saw %d eu event(s) — the streams are cross-wired", seen)
	}

	// The eu Bus itself must see its own event (sanity: scoping didn't break it).
	sctx, scancel := context.WithTimeout(ctx, 2*time.Second)
	defer scancel()
	got := 0
	_ = eu.Tail(sctx, "run-xcell", func(types.RunEvent) error {
		got++
		scancel() // stop after the first
		return nil
	})
	if got == 0 {
		t.Fatal("eu Cell did not see its own event — scoped publish/tail broken")
	}
}
