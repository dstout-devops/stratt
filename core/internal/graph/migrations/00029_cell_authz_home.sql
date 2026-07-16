-- +goose Up
-- The authz-home Cell (charter §1.6, ADR-0044 slice 4): the ONE Cell whose
-- leader syncs the global OpenFGA tuple store. Exactly one across a named fleet
-- (validated at CaC compile) — every other Cell reads authz but never writes
-- tuples, so N Cells sharing one OpenFGA cannot thrash each other's grants.
-- Default false; the built-in single-Cell 'local' (never a row here) is the
-- trivial authz writer by construction when no named Cells are declared.
ALTER TABLE graph.cell ADD COLUMN authz_home boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE graph.cell DROP COLUMN authz_home;
