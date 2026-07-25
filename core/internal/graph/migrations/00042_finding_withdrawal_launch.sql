-- +goose Up
-- A Finding's OWN launch spec (ADR-0120 D1), for the Findings whose spec no Baseline can hold.
--
-- Two cases, both permanent and for different reasons:
--
--   ORPHAN    — the Baseline existed and is PRUNED by the same Apply that writes the Finding,
--               because a Baseline whose Assignment is withdrawn must stop being observed.
--               graph.finding.baseline has no foreign key (deliberately, 00010), so the Finding
--               survives pointing at a row that is gone. Its spec here is the ONLY surviving
--               record — write-once, immutable.
--   PROVISION — there was never a Baseline. `provision/<intent>` is a synthetic grouping name,
--               and a real Baseline would be a compiled expectation over something that does not
--               exist yet (ADR-0058 M1, §1.2). Its spec here is DERIVED from Git and must be
--               REDERIVED every reconcile — see WriteProvisionFinding's DO UPDATE, which has to
--               refresh these columns or an open Finding serves its first pass's values forever.
--
-- launch_kind is the SINGLE branch point for what launches (remediate|remove|build). framework
-- used to double as one — it carries 'orphan' and 'provision' and the launch door branched on it
-- — and two discriminators that can disagree, resolved by whichever branch runs first, is the
-- implicit precedence §2.4 forbids.
--
-- Typed columns rather than scraping graph.finding.diff, which already carries the same values for
-- human consumption. diff is a display blob — documented as redacted and size-capped — and making
-- a launch depend on it would mean any future capping silently breaks the ability to retire or
-- build, with no failing test to notice (§1.8).
--
-- ADDITIVE ONLY, so ADR-0078's pre-upgrade migration window is safe with no expand/contract
-- marker: every column is nullable with no default, and the previous release's replicas simply
-- never write them.
--
-- NOTE FOR ANYONE WHOSE DEV DATABASE PREDATES THIS EDIT. This file previously created
-- remove_workflow / remove_params; ADR-0120 D1 renamed them before release, and the file was
-- edited in place rather than renamed by a new migration, because no deployment has ever run it
-- (origin/main stops at 00040). goose will not re-run an applied version, so a dev database that
-- got the old names keeps them: `task dev:down && task dev:up`. The TEST suite is unaffected —
-- testStore creates a throwaway database and migrates it from scratch, so it always sees this
-- file's current content. Against a RELEASED schema this
-- would have had to be a fresh migration that renames the columns, carrying ADR-0078's
-- expand/contract acknowledgement and a roll window where old replicas still write the old names.
-- That difference is the whole reason the rename was worth doing now rather than later.
--
-- (Deliberately not spelling the SQL rename keywords above: `task migrate:lint` text-matches the
-- Up section for destructive statements and cannot tell prose from SQL, so naming them in a comment
-- would fail the gate and invite an expand/contract marker this additive migration must not carry.)
ALTER TABLE graph.finding
    ADD COLUMN IF NOT EXISTS launch_workflow text,
    ADD COLUMN IF NOT EXISTS launch_params jsonb,
    ADD COLUMN IF NOT EXISTS launch_kind text;

-- The closed set from types.ValidLaunchKind, constrained in the schema rather than trusted to the
-- app: invalid state must not be projectable (data-layer rule, §1.2). NULL is valid and means
-- "this Finding's spec lives on its Baseline", which is the common case.
ALTER TABLE graph.finding
    ADD CONSTRAINT finding_launch_kind_known
    CHECK (launch_kind IS NULL OR launch_kind IN ('remediate', 'remove', 'build'));

-- A launch spec is all-or-nothing: a kind with no Workflow cannot be launched, and a Workflow with
-- no kind cannot be described to the operator approving it (§1.8). Params stay optional — plenty of
-- Workflows need none.
ALTER TABLE graph.finding
    ADD CONSTRAINT finding_launch_spec_complete
    CHECK ((launch_kind IS NULL) = (launch_workflow IS NULL));

-- +goose Down
-- Reversible: dropping the columns and their constraints restores the pre-change shape exactly.
-- Written explicitly per ADR-0078 follow-up MIG-1 — a migration without a Down is a one-way door,
-- and this is not one. Findings written while it was present lose their launch spec and fall back
-- to being readable-only in diff, which is the pre-change behaviour.
ALTER TABLE graph.finding
    DROP CONSTRAINT IF EXISTS finding_launch_spec_complete,
    DROP CONSTRAINT IF EXISTS finding_launch_kind_known,
    DROP COLUMN IF EXISTS launch_kind,
    DROP COLUMN IF EXISTS launch_params,
    DROP COLUMN IF EXISTS launch_workflow;
