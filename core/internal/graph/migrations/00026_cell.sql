-- +goose Up
-- Cells — region-local, single-writer control-plane shards (charter §2.3 Named
-- Kind; ADR-0044, realizing ADR-0040 §4). A Cell is a CaC-declared PROJECTION,
-- not core-model attribute state (§1.2): the declaration lives in Git, the sole
-- writer is the desired-state engine (like Site/View/Trigger/Emitter). It holds
-- NO secret material. The built-in "local" Cell is never a row here — mirroring
-- graph.site and the LocalSite convention. This slice introduces the concept +
-- identity + homing COLUMNS; homing SEMANTICS (the mgmt.cell residency Facet,
-- the CaC-vs-observed authority rule, Finding-on-mismatch) are ADR-0044 slice 2.
CREATE TABLE graph.cell (
    name            text PRIMARY KEY CHECK (name <> 'local'),
    region          text NOT NULL,
    -- The Cell's strattd API address, for the federation router's fan-out.
    endpoint        text NOT NULL,
    dispatch_prefix text,
    description     text,
    declared_by     text NOT NULL DEFAULT 'cac' CHECK (declared_by IN ('cac', 'api')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER cell_touch_updated_at
    BEFORE UPDATE ON graph.cell
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- Homing columns (nullable ⇒ the built-in "local" Cell), mirroring how an unset
-- mgmt.site ⇒ local. A Site belongs to one Cell; a Source (and the Entities it
-- projects) home to one Cell; a Run homes to one Cell and may touch a union.
ALTER TABLE graph.site   ADD COLUMN cell text;
ALTER TABLE graph.source ADD COLUMN cell text;
ALTER TABLE graph.run    ADD COLUMN cell  text;
ALTER TABLE graph.run    ADD COLUMN cells jsonb;

-- Which Cell wrote each event/attribute (§2.1 provenance, one level up from
-- writer kind/ref). Default 'local' keeps every existing row and write valid —
-- no hot-path behavior change; the write path stamps STRATT_CELL_ID.
ALTER TABLE audit.event   ADD COLUMN cell text NOT NULL DEFAULT 'local';
ALTER TABLE graph.entity   ADD COLUMN prov_cell text NOT NULL DEFAULT 'local';
ALTER TABLE graph.relation ADD COLUMN prov_cell text NOT NULL DEFAULT 'local';
ALTER TABLE graph.facet    ADD COLUMN prov_cell text NOT NULL DEFAULT 'local';

-- +goose Down
ALTER TABLE graph.facet    DROP COLUMN prov_cell;
ALTER TABLE graph.relation DROP COLUMN prov_cell;
ALTER TABLE graph.entity   DROP COLUMN prov_cell;
ALTER TABLE audit.event    DROP COLUMN cell;
ALTER TABLE graph.run    DROP COLUMN cells;
ALTER TABLE graph.run    DROP COLUMN cell;
ALTER TABLE graph.source DROP COLUMN cell;
ALTER TABLE graph.site   DROP COLUMN cell;
DROP TABLE graph.cell;
