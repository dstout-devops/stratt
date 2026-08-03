package graph

import (
	"context"
	"fmt"
	"time"
)

// ── the Trigger engine's memory of its own recent past (ADR-0162) ───────────────────────────────
//
// NOT a projection, and the whole file is written to keep it that way: no Entity id crosses this
// boundary, no Facet is written, and nothing returned here is a fact about a host. It describes the
// EVENT STREAM. See migration 00051 for the argument in full.

// TriggerWindow is one Trigger's state for one correlation key.
type TriggerWindow struct {
	OpenedAt    time.Time
	MatchCount  int
	Satisfied   []int32
	LastFiredAt *time.Time
}

// TriggerWindowAdvance records one matching event and returns the window AFTER the write, so the
// caller decides to fire from a value the database agreed to — never from a read it did separately.
//
// ONE STATEMENT, and it is the concurrency design rather than a style choice: two replicas
// consuming the same emitter stream will race on exactly this row, and a read-then-write in the
// engine would let both see "4 of 5" and both fire. The upsert makes the increment atomic, so the
// fifth event has exactly one winner.
//
// `within` expiry is applied HERE too: a window older than the declared span is reopened rather than
// continued, because a count that spanned an unbounded period would fire on five events a week apart
// and call it a storm.
func (s *Store) TriggerWindowAdvance(ctx context.Context, trigger, key string, within time.Duration, satisfiedIdx int) (TriggerWindow, error) {
	var w TriggerWindow
	// The CASE arms are the reopen decision: an expired window starts again at this event (count 1,
	// this condition only), a live one accumulates.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO graph.trigger_window (trigger_name, correlation_key, opened_at, match_count, satisfied)
		VALUES ($1, $2, now(), 1, CASE WHEN $4 >= 0 THEN ARRAY[$4::int] ELSE '{}'::int[] END)
		ON CONFLICT (trigger_name, correlation_key) DO UPDATE SET
			opened_at = CASE
				WHEN $3::interval IS NOT NULL AND now() - graph.trigger_window.opened_at > $3::interval
				THEN now() ELSE graph.trigger_window.opened_at END,
			match_count = CASE
				WHEN $3::interval IS NOT NULL AND now() - graph.trigger_window.opened_at > $3::interval
				THEN 1 ELSE graph.trigger_window.match_count + 1 END,
			satisfied = CASE
				WHEN $3::interval IS NOT NULL AND now() - graph.trigger_window.opened_at > $3::interval
				THEN (CASE WHEN $4 >= 0 THEN ARRAY[$4::int] ELSE '{}'::int[] END)
				WHEN $4 >= 0 AND NOT (graph.trigger_window.satisfied @> ARRAY[$4::int])
				THEN graph.trigger_window.satisfied || ARRAY[$4::int]
				ELSE graph.trigger_window.satisfied END
		RETURNING opened_at, match_count, satisfied, last_fired_at`,
		trigger, key, nullableInterval(within), satisfiedIdx,
	).Scan(&w.OpenedAt, &w.MatchCount, &w.Satisfied, &w.LastFiredAt)
	if err != nil {
		return w, fmt.Errorf("graph: advance trigger window %s/%s: %w", trigger, key, err)
	}
	return w, nil
}

// TriggerWindowFired stamps the fire and RESETS the window (ADR-0162 D3).
//
// Reset by deletion rather than by zeroing, so the next event opens a window at its own arrival —
// which is what "five within ten minutes, again" means. The cooldown fact survives the reset because
// it is written back on the fresh row; a cooldown that vanished when the window reset would be the
// storm damping switching itself off exactly during a storm.
func (s *Store) TriggerWindowFired(ctx context.Context, trigger, key string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.trigger_window (trigger_name, correlation_key, opened_at, match_count, satisfied, last_fired_at)
		VALUES ($1, $2, now(), 0, '{}'::int[], now())
		ON CONFLICT (trigger_name, correlation_key) DO UPDATE SET
			opened_at = now(), match_count = 0, satisfied = '{}'::int[], last_fired_at = now()`,
		trigger, key)
	if err != nil {
		return fmt.Errorf("graph: reset trigger window %s/%s: %w", trigger, key, err)
	}
	return nil
}

// TriggerWindowSweep deletes windows untouched for longer than keep — ordinary housekeeping for a
// table whose whole content is transient. A Trigger correlating on a high-cardinality field (one key
// per host, per deploy) would otherwise accumulate a row per value forever.
func (s *Store) TriggerWindowSweep(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM graph.trigger_window
		 WHERE opened_at < now() - $1::interval
		   AND (last_fired_at IS NULL OR last_fired_at < now() - $1::interval)`,
		nullableInterval(keep))
	if err != nil {
		return 0, fmt.Errorf("graph: sweep trigger windows: %w", err)
	}
	return tag.RowsAffected(), nil
}

// nullableInterval renders a duration for Postgres, or NULL when there is no window — which is the
// cooldown-only case, where the row persists until the sweep rather than expiring on its own.
func nullableInterval(d time.Duration) any {
	if d <= 0 {
		return nil
	}
	return fmt.Sprintf("%d seconds", int64(d.Seconds()))
}
