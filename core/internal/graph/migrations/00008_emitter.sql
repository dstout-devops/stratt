-- +goose Up
-- Emitters (charter §2.2, ADR-0018): CaC-declared event ingest points. The
-- spec holds only a token HASH — no secret material exists in this table or
-- the declarations repo (§2.5).
CREATE TABLE graph.emitter (
    name       text PRIMARY KEY,
    kind       text NOT NULL CHECK (kind IN ('webhook', 'alertmanager')),
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER emitter_touch_updated_at
    BEFORE UPDATE ON graph.emitter
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- The §1.8 Trigger → WorkflowRun descent rung (workflow-launching Triggers,
-- ADR-0018). Null = API-started executions.
ALTER TABLE graph.workflow_run ADD COLUMN triggered_by text;

-- +goose Down
ALTER TABLE graph.workflow_run DROP COLUMN triggered_by;
DROP TABLE graph.emitter;
