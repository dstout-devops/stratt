-- +goose Up
-- Relation liveness (charter §1.2/§2.4, ADR-0082): the per-Source presence set that
-- turns an observed edge's liveness from a last-writer-wins scalar into a UNION over
-- Sources — the edge analog of graph.entity_presence (ADR-0042). An edge is live
-- while >=1 Source still asserts it; a Source's full-sync sweep retracts only ITS
-- presence and deletes the edge only when its LAST presence row is gone. This closes
-- the ADR-0059 relation-GC gap so multi-source relations (ADR-0081 depends-on) are
-- correct. Run-provenance edges (a build's placed-in) write no presence and keep
-- their existing RetractRunRelationsFrom + endpoint-cascade lifecycle.
CREATE TABLE graph.relation_presence (
    relation_id uuid NOT NULL REFERENCES graph.relation (id) ON DELETE CASCADE,
    source_id   uuid NOT NULL REFERENCES graph.source (id) ON DELETE CASCADE,
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (relation_id, source_id)
);

-- The PK's leading relation_id covers the last-presence-gone check; this index
-- covers the source_id FK (ON DELETE CASCADE from graph.source) and the per-Source
-- full-sync presence sweep.
CREATE INDEX relation_presence_source_idx ON graph.relation_presence (source_id);

-- Best-effort backfill for existing observed edges (mirrors ADR-0042): only the
-- last-writer Source is reconstructable — historical multi-Source co-assertion was
-- never stored — so a co-asserted edge holds a single presence row until each Source
-- re-syncs, a transient window IDENTICAL to today's last-writer behavior and
-- self-healing after one full cycle per Source. Run-provenance edges (empty/NULL
-- prov_source_id) and any non-uuid legacy value are excluded.
INSERT INTO graph.relation_presence (relation_id, source_id, first_seen, last_seen)
SELECT id, prov_source_id::uuid, now(), now()
FROM graph.relation
WHERE prov_source_id IS NOT NULL
  AND prov_source_id <> ''
  AND prov_source_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
ON CONFLICT DO NOTHING;

-- Presence carries no provenance columns, so it gets a dedicated write-path trigger
-- that gates the §1.2 write path WITHOUT the prov-kind check (the shared
-- enforce_write_path's prov check assumes provenance columns this table lacks).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.enforce_write_path_presence() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    wp text := current_setting('stratt.write_path', true);
BEGIN
    IF wp IS NULL OR wp NOT IN ('normalizer', 'run-provenance') THEN
        RAISE EXCEPTION USING
            errcode = 'P0001',
            message = format(
                'write to %s.%s rejected: only Normalizers and Run provenance may write the graph projection (charter §1.2)',
                TG_TABLE_SCHEMA, TG_TABLE_NAME);
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER relation_presence_write_path
    BEFORE INSERT OR UPDATE OR DELETE ON graph.relation_presence
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_write_path_presence();

-- +goose Down
DROP TRIGGER relation_presence_write_path ON graph.relation_presence;
DROP FUNCTION graph.enforce_write_path_presence();
DROP TABLE graph.relation_presence;
