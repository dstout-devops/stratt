-- +goose Up
-- Cross-source Entity liveness (charter §1.2/§2.4, ADR-0042): the per-Source
-- presence set that turns whole-Entity liveness from a last-writer-wins scalar
-- into a UNION over Sources. An Entity is live while >=1 Source still observes
-- it; TombstoneAbsent/TombstoneByIdentity now retract only the calling Source's
-- presence and tombstone the Entity only when its LAST presence row is gone.
-- The Projector records a row here on every Syncer upsert (run writes record
-- none — run-only Entities stay outside presence and are never tombstoned).
-- entity.deleted_at stays the read gate (readers + the 50k View gate untouched);
-- this table also backs the observedBy Entity read surface.
CREATE TABLE graph.entity_presence (
    entity_id  uuid NOT NULL REFERENCES graph.entity (id) ON DELETE CASCADE,
    source_id  uuid NOT NULL REFERENCES graph.source (id) ON DELETE CASCADE,
    first_seen timestamptz NOT NULL DEFAULT now(),
    last_seen  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_id, source_id)
);

-- The PK's leading entity_id covers the reconcile join + NOT EXISTS; this index
-- covers the source_id FK (ON DELETE CASCADE from graph.source) and any future
-- per-Source presence sweep.
CREATE INDEX entity_presence_source_idx ON graph.entity_presence (source_id);

-- Best-effort backfill for existing live Entities, BEFORE the write-path trigger
-- exists (so this INSERT isn't gated). Only the last-writer Source is
-- reconstructable — historical multi-Source presence was never stored — so a
-- co-managed host holds a single presence row until each Source re-syncs: a
-- transient window IDENTICAL to today's last-writer behavior, self-healing after
-- one full cycle per Source. Run-provenance rows (empty prov_source_id) and any
-- non-uuid legacy value are excluded.
INSERT INTO graph.entity_presence (entity_id, source_id, first_seen, last_seen)
SELECT id, prov_source_id::uuid, now(), now()
FROM graph.entity
WHERE deleted_at IS NULL
  AND prov_source_id IS NOT NULL
  AND prov_source_id <> ''
  AND prov_source_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
ON CONFLICT DO NOTHING;

-- Presence carries no provenance columns (like entity_identity), so exempt it
-- from enforce_write_path's prov-kind check while keeping the write-path gate.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.enforce_write_path() RETURNS trigger
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
    -- Provenance must agree with the declared write path (§2.1: exactly one
    -- answer): the normalizer path stamps syncer provenance, the
    -- run-provenance path stamps run provenance.
    IF TG_TABLE_NAME NOT IN ('entity_identity', 'entity_presence') THEN
        DECLARE
            pk text := to_jsonb(NEW) ->> 'prov_writer_kind';
        BEGIN
            IF (wp = 'normalizer' AND pk <> 'syncer')
               OR (wp = 'run-provenance' AND pk <> 'run') THEN
                RAISE EXCEPTION USING
                    errcode = 'P0001',
                    message = format(
                        'write path %s cannot stamp provenance writer kind %s (charter §2.1)',
                        wp, pk);
            END IF;
        END;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER entity_presence_write_path
    BEFORE INSERT OR UPDATE OR DELETE ON graph.entity_presence
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_write_path();

-- +goose Down
DROP TRIGGER entity_presence_write_path ON graph.entity_presence;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.enforce_write_path() RETURNS trigger
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
    IF TG_TABLE_NAME <> 'entity_identity' THEN
        DECLARE
            pk text := to_jsonb(NEW) ->> 'prov_writer_kind';
        BEGIN
            IF (wp = 'normalizer' AND pk <> 'syncer')
               OR (wp = 'run-provenance' AND pk <> 'run') THEN
                RAISE EXCEPTION USING
                    errcode = 'P0001',
                    message = format(
                        'write path %s cannot stamp provenance writer kind %s (charter §2.1)',
                        wp, pk);
            END IF;
        END;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TABLE graph.entity_presence;
