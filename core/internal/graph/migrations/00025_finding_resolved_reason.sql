-- +goose Up
-- Finding resolution reason (charter §1.8, ADR-0043): distinguishes WHY a
-- Finding resolved so descent shows it plainly — 'observed-clean' (the drift
-- went away) vs 'entity-tombstoned' (the Entity the Finding was about no longer
-- exists, e.g. a renewed/revoked cert whose serial changed identity). Nullable;
-- a NULL on a legacy resolved row reads as observed-clean.
ALTER TABLE graph.finding ADD COLUMN resolved_reason text;

-- +goose Down
ALTER TABLE graph.finding DROP COLUMN resolved_reason;
