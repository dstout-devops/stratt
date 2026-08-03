package graph

import (
	"context"
	"testing"
	"time"
)

// ── ADR-0162 · the engine's memory of its own recent past ────────────────────────────────
//
// These run against a REAL Postgres because the semantics being tested are the database's: an
// atomic upsert-increment, an interval comparison, and an array containment. A fake would test the
// test.

func TestWindowAccumulatesAndResets(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const trig, key = "flap", ""
	within := time.Minute

	// Five matching events accumulate in one window.
	for want := 1; want <= 5; want++ {
		w, err := s.TriggerWindowAdvance(ctx, trig, key, within, -1)
		if err != nil {
			t.Fatal(err)
		}
		if w.MatchCount != want {
			t.Fatalf("event %d: count = %d, want %d", want, w.MatchCount, want)
		}
	}

	// Firing RESETS the window (D3): the next event opens a new one at its own arrival, so a storm
	// produces ONE Run rather than one per event past the threshold.
	if err := s.TriggerWindowFired(ctx, trig, key); err != nil {
		t.Fatal(err)
	}
	w, err := s.TriggerWindowAdvance(ctx, trig, key, within, -1)
	if err != nil {
		t.Fatal(err)
	}
	if w.MatchCount != 1 {
		t.Fatalf("after firing, the window restarts at 1, got %d", w.MatchCount)
	}
	// …and the cooldown fact SURVIVES the reset. A cooldown that vanished when the window reset
	// would be the storm damping switching itself off exactly during a storm.
	if w.LastFiredAt == nil {
		t.Fatal("last_fired_at must survive the window reset — it is the cooldown")
	}
}

// A window older than the declared span is REOPENED, not continued: five events a week apart is not
// a storm, and counting them as one would fire on a pattern nobody has.
func TestAnExpiredWindowReopensRatherThanContinuing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const trig, key = "slow", ""

	if _, err := s.TriggerWindowAdvance(ctx, trig, key, time.Hour, -1); err != nil {
		t.Fatal(err)
	}
	// A window of one nanosecond is expired by the time the next statement runs.
	w, err := s.TriggerWindowAdvance(ctx, trig, key, time.Nanosecond, -1)
	if err != nil {
		t.Fatal(err)
	}
	if w.MatchCount != 1 {
		t.Fatalf("an expired window restarts at 1, got %d", w.MatchCount)
	}
}

// Correlation keys keep unrelated events apart (D4) — the property that makes `allOf` safe. Two
// services flapping at once are two windows, not one.
func TestCorrelationKeysDoNotShareAWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const trig = "deploys"

	for i := 0; i < 3; i++ {
		if _, err := s.TriggerWindowAdvance(ctx, trig, "checkout", time.Minute, -1); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.TriggerWindowAdvance(ctx, trig, "billing", time.Minute, -1)
	if err != nil {
		t.Fatal(err)
	}
	if other.MatchCount != 1 {
		t.Fatalf("a different correlation key is a different window: got %d, want 1", other.MatchCount)
	}
}

// `allOf` records WHICH conditions have been satisfied, by index, and does not double-count one
// condition satisfied twice — otherwise two 'deploy finished' events would look like a deploy AND a
// failed health check.
func TestSatisfiedConditionsAreASetNotACount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const trig, key = "deploy-then-fail", "checkout"

	w, err := s.TriggerWindowAdvance(ctx, trig, key, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Satisfied) != 1 {
		t.Fatalf("first condition: %v", w.Satisfied)
	}
	// The SAME condition again must not advance the pattern.
	if w, err = s.TriggerWindowAdvance(ctx, trig, key, time.Minute, 0); err != nil {
		t.Fatal(err)
	} else if len(w.Satisfied) != 1 {
		t.Fatalf("one condition satisfied twice is still one condition: %v", w.Satisfied)
	}
	// A different one does.
	if w, err = s.TriggerWindowAdvance(ctx, trig, key, time.Minute, 1); err != nil {
		t.Fatal(err)
	} else if len(w.Satisfied) != 2 {
		t.Fatalf("a second distinct condition advances the pattern: %v", w.Satisfied)
	}
}

// The sweep bounds a table whose whole content is transient. A Trigger correlating on a
// high-cardinality field would otherwise keep a row per value forever.
func TestSweepRemovesStaleWindows(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.TriggerWindowAdvance(ctx, "old", "k", time.Hour, -1); err != nil {
		t.Fatal(err)
	}
	n, err := s.TriggerWindowSweep(ctx, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("a window older than the retention must be swept, removed %d", n)
	}
}
