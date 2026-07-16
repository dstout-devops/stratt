-- +goose Up
-- Destination-side home-ownership gate (charter §1.2/§2.1, ADR-0045). The slice-7
-- seal fence (00031) makes the SOURCE Cell stop projecting a sealed Source; this
-- makes the DESTINATION side a DB constraint too: a Normalizer projection whose
-- Source is homed on a DIFFERENT Cell than the projecting daemon is rejected.
--
-- Why a trigger and not just Go: a standby Connector deployed on a peer Cell must
-- not project a Source another Cell homes — and per graph-data-layer.md that
-- single-writer guarantee has to live in the data layer, not a code-review norm
-- (charter-guardian ADR-0045 must-fix 1). It closes the steady-state half of
-- ADR-0044 residual tension #4 (single-writer no longer leans on protocol once a
-- Source is homed): the projector declares its Cell as `stratt.cell` and the
-- trigger rejects any Normalizer write for a Source homed elsewhere.
--
-- Byte-identical for the single-Cell default: the home check fires ONLY when the
-- projecting daemon is a NAMED Cell (stratt.cell not '' / 'local'); a 'local'
-- daemon projecting 'local'-homed Sources is unaffected, exactly as every prior
-- ADR-0044 slice preserves. Folded into the seal fence's existing source lookup —
-- no extra per-write query.
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
    -- Seal fence (00031) + home gate (this migration): one lookup, two checks.
    IF wp = 'normalizer' THEN
        DECLARE
            sid    text := CASE TG_TABLE_NAME
                WHEN 'entity_presence' THEN to_jsonb(NEW) ->> 'source_id'
                ELSE to_jsonb(NEW) ->> 'prov_source_id'
            END;
            dcell  text := current_setting('stratt.cell', true);
            srehome text;
            scell  text;
        BEGIN
            IF sid IS NOT NULL AND sid <> '' THEN
                SELECT cell, rehoming_to INTO scell, srehome
                FROM graph.source WHERE id = sid::uuid;
                -- Seal fence: the Source is mid cross-Cell re-home.
                IF srehome IS NOT NULL THEN
                    RAISE EXCEPTION USING
                        errcode = 'P0001',
                        message = format(
                            'write to graph.%s rejected: source %s is sealed for cross-Cell re-home (ADR-0044 slice 7)',
                            TG_TABLE_NAME, sid);
                END IF;
                -- Home gate: reject a Normalizer projection for a Source a NAMED
                -- PEER Cell homes (a standby Connector must not steal it). Fires
                -- only when BOTH the projecting daemon and the Source's home are
                -- named Cells and they differ. An unclaimed / 'local' Source is
                -- claim-by-projection (legacy + single-Cell byte-identical): in a
                -- named fleet every Source is registered to a named home, so a
                -- 'local'-homed Source can never be a named peer's — allowing it is
                -- safe and keeps a 'local' daemon byte-identical.
                IF dcell IS NOT NULL AND dcell <> '' AND dcell <> 'local'
                   AND scell IS NOT NULL AND scell <> '' AND scell <> 'local'
                   AND scell <> dcell THEN
                    RAISE EXCEPTION USING
                        errcode = 'P0001',
                        message = format(
                            'write to graph.%s rejected: source %s is homed on peer cell %s, not this daemon''s cell %s (ADR-0045 home gate)',
                            TG_TABLE_NAME, sid, scell, dcell);
                END IF;
            END IF;
        END;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- Restore the 00031 version (seal fence only, no home gate).
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
