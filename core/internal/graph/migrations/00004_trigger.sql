-- +goose Up
-- Triggers (charter §2, ADR-0010): anything that starts a Run. v1 kind is
-- schedule (a Temporal Schedule actuates it). The row is the graph-store
-- projection of the Git declaration (§1.2 — rebuildable, CaC-only in v1);
-- the Temporal Schedule is a further projection reconciled from this table.
CREATE TABLE graph.trigger (
    name       text PRIMARY KEY,
    kind       text NOT NULL CHECK (kind IN ('schedule')),
    -- The full declaration (cron, paused, launch parameters) — declaration-
    -- shaped data, compared whole for reconcile diffs.
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trigger_touch_updated_at
    BEFORE UPDATE ON graph.trigger
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- The §1.8 descent rung Trigger → Run must be queryable at Run creation,
-- not buried in a terminal summary. Null = manual/API launch.
ALTER TABLE graph.run ADD COLUMN triggered_by text;

-- +goose Down
ALTER TABLE graph.run DROP COLUMN triggered_by;
DROP TABLE graph.trigger;
