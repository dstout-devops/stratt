-- +goose Up
-- Fenced cross-Cell Source re-home (charter §2.1/§2.4, ADR-0044 slice 7). The
-- unit of re-home is the SOURCE, not the Entity: an Entity is a projection of a
-- Source (charter §1.2), so moving an Entity while its Source keeps syncing on
-- the old Cell would silently re-project it there — a durable second writer. We
-- instead seal the Source on its home Cell, hand it to a peer Cell, let the peer
-- RE-PROJECT its Entities (rebuildable, not shipped), and tombstone the old
-- Cell's now-unobserved Entities. (charter-guardian slice-7 must-fix 1/2.)

-- rehoming_to: NULL ⇒ settled (the byte-identical single-Cell default); a Cell
-- name ⇒ this Source is SEALED and being moved there. home_epoch is a per-Source
-- fencing token, bumped on every seal so a peer can reject a stale/replayed
-- adopt (idempotency); the true single-writer ordering authority is the Temporal
-- RehomeSourceWorkflow's linear history, not a cross-DB compare (there is no
-- cross-Postgres CAS — must-fix 4).
ALTER TABLE graph.source ADD COLUMN rehoming_to text;
ALTER TABLE graph.source ADD COLUMN home_epoch  bigint NOT NULL DEFAULT 0;

-- Costs nothing for a settled (single-Cell) estate: the partial index is empty
-- until a Source is actually sealed; the seal fence's per-write lookup is a PK hit.
CREATE INDEX source_rehoming_to ON graph.source (rehoming_to) WHERE rehoming_to IS NOT NULL;

-- Extend the write-path gate with (a) a 'rehome' mover path and (b) the SEAL
-- FENCE: a Normalizer projection that stamps a SEALED Source is REJECTED, so
-- once a Source is sealed its home Cell physically cannot keep projecting its
-- Entities — the fence is a DB CONSTRAINT, not just protocol (closes ADR-0044
-- residual tension #4 for the re-home window). The 'rehome' mover is exempt (it
-- performs the seal + the old-Cell tombstone); run-provenance is unaffected.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.enforce_write_path() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    wp text := current_setting('stratt.write_path', true);
BEGIN
    IF wp IS NULL OR wp NOT IN ('normalizer', 'run-provenance', 'rehome') THEN
        RAISE EXCEPTION USING
            errcode = 'P0001',
            message = format(
                'write to %s.%s rejected: only Normalizers, Run provenance, and the re-home mover may write the graph projection (charter §1.2)',
                TG_TABLE_SCHEMA, TG_TABLE_NAME);
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    -- Provenance must agree with the declared write path (§2.1). The 'rehome'
    -- mover only sets deleted_at on rows that already carry their provenance, so
    -- it is exempt from the kind cross-check (it never fabricates provenance).
    IF wp IN ('normalizer', 'run-provenance')
       AND TG_TABLE_NAME NOT IN ('entity_identity', 'entity_presence') THEN
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
    -- Seal fence (ADR-0044 slice 7): reject a Normalizer projection whose Source
    -- is sealed for cross-Cell re-home. entity_presence carries source_id; the
    -- projection tables carry prov_source_id.
    IF wp = 'normalizer' THEN
        DECLARE
            sid text := CASE TG_TABLE_NAME
                WHEN 'entity_presence' THEN to_jsonb(NEW) ->> 'source_id'
                ELSE to_jsonb(NEW) ->> 'prov_source_id'
            END;
        BEGIN
            IF sid IS NOT NULL AND sid <> '' AND EXISTS (
                SELECT 1 FROM graph.source WHERE id = sid::uuid AND rehoming_to IS NOT NULL
            ) THEN
                RAISE EXCEPTION USING
                    errcode = 'P0001',
                    message = format(
                        'write to graph.%s rejected: source %s is sealed for cross-Cell re-home (ADR-0044 slice 7)',
                        TG_TABLE_NAME, sid);
            END IF;
        END;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX graph.source_rehoming_to;
ALTER TABLE graph.source DROP COLUMN home_epoch;
ALTER TABLE graph.source DROP COLUMN rehoming_to;

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
