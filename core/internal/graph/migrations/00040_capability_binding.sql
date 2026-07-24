-- +goose Up
-- Capability→provider bindings (ADR-0110 D3): the CaC selection of WHICH verified provider
-- fulfils a capability class for a given Intent kind, so an Intent's `requires: [provisioning]`
-- resolves to a concrete provider + build Action (ADR-0110 D1/D4). NOT a Named Kind (§2 frozen) —
-- a declaration FORM the capability registry reconciles, exactly as graph.actuator / graph.connector
-- (00038) project their Git declarations (§1.2 — rebuildable, CaC-only; the desired-state reconcile
-- engine is the SOLE writer, so there is no declared_by column and no API write path).
--
-- Distinct from graph.capability_provider (00039), which is the runtime VERIFIED-provider projection:
-- that table records which providers a leader dialed and verified; THIS table records the operator's
-- SELECTION among them. Resolution (ADR-0110 D4) reads both — a binding only counts if its provider
-- is in the verified index.

CREATE TABLE graph.capability_binding (
    name       text PRIMARY KEY,
    -- The full declaration (its entries + environment scope) — compared whole for reconcile diffs.
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER capability_binding_touch_updated_at
    BEFORE UPDATE ON graph.capability_binding
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- +goose Down
DROP TABLE graph.capability_binding;
