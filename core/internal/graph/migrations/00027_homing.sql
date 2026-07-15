-- +goose Up
-- Cell homing residency (charter §2.1/§2.4, ADR-0044 slice 2). home_cell is the
-- Cell an Entity LIVES in — a stable, single-writer residency, set once at
-- creation (= the creating daemon's Cell) and NEVER overwritten on a later
-- re-observation; the only mutation is the fenced re-home (ADR-0044 slice 7).
-- This is deliberately a set-once COLUMN, not a Facet: a Facet is last-writer
-- (ON CONFLICT DO UPDATE), which would silently resolve a cross-Cell stray write
-- to match the writer — defeating the §2.4 placement-mismatch Finding that must
-- instead SURFACE that divergence. Default 'local' backfills every existing row;
-- a single-Cell deployment is a no-op. (source.cell / run.cell / run.cells were
-- added in 00026.)
ALTER TABLE graph.entity ADD COLUMN home_cell text NOT NULL DEFAULT 'local';

-- Supports the placement sweep and the slice-3 router's per-Entity home lookup;
-- partial so it costs nothing for the single-Cell 'local' estate.
CREATE INDEX entity_home_cell ON graph.entity (home_cell) WHERE home_cell <> 'local';

-- +goose Down
DROP INDEX entity_home_cell;
ALTER TABLE graph.entity DROP COLUMN home_cell;
