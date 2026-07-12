-- +goose Up
-- Intent layer (charter §2.4, ADR-0023): Intents, Assignments, Blueprints —
-- the team-facing declarative surface that compiles into Baselines. All
-- CaC-only projections of the Git declaration, mirroring trigger/workflow/
-- emitter/baseline/mcp_server. The compiler reads these + live View
-- membership and emits compiled Baselines into graph.baseline.
CREATE TABLE graph.intent (
    name       text PRIMARY KEY,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER intent_touch_updated_at
    BEFORE UPDATE ON graph.intent
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

CREATE TABLE graph.assignment (
    name       text PRIMARY KEY,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER assignment_touch_updated_at
    BEFORE UPDATE ON graph.assignment
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- Blueprints are versioned (§2.4: Assignments pin a version); (name,
-- version) is the identity so upgrades roll through rings alongside old
-- versions.
CREATE TABLE graph.blueprint (
    name       text NOT NULL,
    version    int  NOT NULL,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (name, version)
);
CREATE TRIGGER blueprint_touch_updated_at
    BEFORE UPDATE ON graph.blueprint
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- Per-Assignment compiled-membership snapshot — the state the membership-
-- delta plan diffs against and the max-delta gate reads (§4.3). acked_delta
-- records the AckDelta value under which the last over-threshold delta was
-- applied, so a paused compile unblocks only on a deliberate Git bump.
CREATE TABLE graph.assignment_membership (
    assignment   text PRIMARY KEY,
    entity_ids   text[] NOT NULL DEFAULT '{}',
    member_count int NOT NULL DEFAULT 0,
    acked_delta  int NOT NULL DEFAULT 0,
    compiled_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE graph.assignment_membership;
DROP TABLE graph.blueprint;
DROP TABLE graph.assignment;
DROP TABLE graph.intent;
