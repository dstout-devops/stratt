-- +goose Up
-- Baselines + Findings v1 (charter §2.4, §8 Phase 2, ADR-0019).

-- graph.baseline is the projection of the Git declaration (CaC-only,
-- mirroring trigger/workflow/emitter); the Temporal Schedule driving its
-- cadence is a further projection reconciled from this table.
CREATE TABLE graph.baseline (
    name       text PRIMARY KEY,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER baseline_touch_updated_at
    BEFORE UPDATE ON graph.baseline
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- graph.finding is a drift/compliance result (§2.4): Entity + Baseline +
-- observed-vs-expected diff + severity + Evidence ref (run_id). Findings are
-- derived from check Runs — NOT Entity/Facet attributes, so they live off
-- the projector write path; severity/framework are pinned from the Baseline
-- at observation time (audit: the declaration that raised it, even if the
-- Baseline changes later).
CREATE TABLE graph.finding (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    baseline            text NOT NULL,
    target              text NOT NULL,
    entity_id           text,
    status              text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'open', 'resolved')),
    severity            text NOT NULL
        CHECK (severity IN ('info', 'warning', 'critical')),
    framework           text NOT NULL DEFAULT '',
    consecutive_drifted int NOT NULL DEFAULT 0,
    diff                jsonb,
    run_id              uuid REFERENCES graph.run (id) ON DELETE SET NULL,
    first_observed      timestamptz NOT NULL DEFAULT now(),
    last_observed       timestamptz NOT NULL DEFAULT now(),
    opened_at           timestamptz,
    resolved_at         timestamptz
);

-- One live (pending|open) Finding per (baseline, target); resolved rows
-- accumulate as the audit history, so a later re-drift opens a fresh row.
CREATE UNIQUE INDEX finding_live_unique
    ON graph.finding (baseline, target) WHERE status <> 'resolved';
CREATE INDEX finding_baseline_status ON graph.finding (baseline, status);

-- The §1.8 Baseline → Run descent rung, queryable at Run creation (the
-- triggered_by pattern). Null = not a Baseline check.
ALTER TABLE graph.run ADD COLUMN baseline text;

-- +goose Down
ALTER TABLE graph.run DROP COLUMN baseline;
DROP TABLE graph.finding;
DROP TABLE graph.baseline;
