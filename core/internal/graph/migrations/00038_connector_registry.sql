-- +goose Up
-- Connector + Actuator registrations (charter §2.2/§2.3, ADR-0103): the CaC desired-state
-- set of enabled integrations, reconciled at runtime into dialed + registered plugins with
-- NO strattd restart. Each row is the graph-store projection of the Git declaration (§1.2 —
-- rebuildable, CaC-only; the desired-state reconcile engine is the SOLE writer, so there is
-- no declared_by column and no API write path — same posture as graph.trigger).
--
-- Two peer Named Kinds, two tables (they are deliberately distinct — §2.2 vs §2.3; a
-- Connector binds a Source, an Actuator runs tool content and binds none). Cross-Kind
-- dispatch-name exclusivity (a Connector and an Actuator under one name) is enforced at
-- registration by the shared orchestrate.PluginRegistry, surfaced not silently tiebroken.

CREATE TABLE graph.connector (
    name       text PRIMARY KEY,
    class      text NOT NULL CHECK (class IN ('syncer', 'action')),
    -- The full declaration (Source binding, grant allowlists, address, interval) — compared
    -- whole for reconcile diffs.
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER connector_touch_updated_at
    BEFORE UPDATE ON graph.connector
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

CREATE TABLE graph.actuator (
    name       text PRIMARY KEY,
    -- The full declaration (address or EE-Job command, action names, tier) — no Source.
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER actuator_touch_updated_at
    BEFORE UPDATE ON graph.actuator
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- +goose Down
DROP TABLE graph.actuator;
DROP TABLE graph.connector;
