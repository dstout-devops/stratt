-- +goose Up
-- The withdrawal launch spec on an ORPHAN Finding (ADR-0118 D3).
--
-- An orphan Finding reports state left behind by a withdrawn Assignment, and the Workflow that
-- retires it needs the values the state was created under. Those values cannot be looked up when
-- they are wanted: the compiler stamps them on the compiled Baseline, and Apply writes the orphan
-- Finding and then PRUNES that Baseline — correctly, since a Baseline whose Assignment is
-- withdrawn must stop being observed. graph.finding.baseline has no foreign key (deliberately,
-- 00010), so the Finding survives pointing at a row that is gone.
--
-- So the orphan Finding carries the spec itself. This is the one place a Finding holds its own
-- launch values rather than reading them from its Baseline: everywhere else that would be a
-- second, staleable copy of a Git-derived fact, and here it is the ONLY copy.
--
-- Typed columns rather than scraping graph.finding.diff, which already carries the same values
-- for human consumption. diff is a display blob — documented as redacted and size-capped — and
-- making a launch depend on it would mean any future capping silently breaks the ability to
-- retire abandoned state, with no failing test to notice (§1.8).
--
-- ADDITIVE ONLY, so ADR-0078's pre-upgrade migration window is safe with no expand/contract
-- marker: both columns are nullable with no default, and the previous release's replicas simply
-- never write them.
ALTER TABLE graph.finding
    ADD COLUMN IF NOT EXISTS remove_workflow text,
    ADD COLUMN IF NOT EXISTS remove_params jsonb;

-- +goose Down
-- Reversible: dropping the columns restores the pre-change shape exactly. Written explicitly per
-- ADR-0078 follow-up MIG-1 — a migration without a Down is a one-way door, and this is not one.
-- Orphan Findings written while it was present lose their launch values and fall back to being
-- readable-only in diff, which is precisely the pre-change behaviour.
ALTER TABLE graph.finding
    DROP COLUMN IF EXISTS remove_workflow,
    DROP COLUMN IF EXISTS remove_params;
