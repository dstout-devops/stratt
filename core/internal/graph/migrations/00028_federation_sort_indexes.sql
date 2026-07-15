-- +goose Up
-- Total-order sort indexes for the cellrouter's k-way merge (ADR-0044 slice 3):
-- ListRuns / ListFindings gained an `id` tiebreak (started_at DESC, id ASC /
-- last_observed DESC, id ASC) so a federated scatter-gather across Cells merges
-- deterministically even when timestamps tie across Cells. These composite
-- indexes keep the widened ORDER BY cheap at scale. Behavior is unchanged for a
-- single Cell — the tiebreak only ever resolves exact-timestamp ties (a
-- determinism improvement regardless of Cells).
CREATE INDEX run_started_at_id_idx ON graph.run (started_at DESC, id);
CREATE INDEX finding_last_observed_id_idx ON graph.finding (last_observed DESC, id);

-- +goose Down
DROP INDEX finding_last_observed_id_idx;
DROP INDEX run_started_at_id_idx;
