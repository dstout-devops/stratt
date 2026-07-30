-- Facet versions are monotonic across an Entity's WHOLE LIFE, including a death and a rebuild.
--
-- graph.facet_history is append-only and keyed (entity_id, namespace, prov_source_id, version).
-- The version came from `OLD.version + 1` on UPDATE and the column default on INSERT — which was
-- correct while a facet row never disappeared. It can now: a revived Entity has its Facets cleared,
-- because the dead instance's state must not be asserted about its replacement (§1.2). The next
-- write then INSERTed at version 1, which history already held, and the whole full sync failed on:
--
--   duplicate key value violates unique constraint "facet_history_pkey"
--
-- so the rebuilt host never got its reach coordinate back. Found on a live floor, not in review.
--
-- The fix is to continue the sequence from HISTORY rather than from the live row. A version is a
-- position in an append-only record, and that record outlives the row — which is the whole point of
-- keeping it (§1.8: descent into what was known, when).
--
-- +goose Up
-- expand/contract-ok: additive only — one function and one BEFORE INSERT trigger. The previous
-- release's replicas keep writing through the same INSERT path and simply get a version they would
-- have computed identically whenever history is empty (UPG-1, ADR-0078).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.facet_version_from_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- Only on INSERT; UPDATE keeps OLD.version + 1 (graph.facet_versioning).
    SELECT coalesce(max(h.version), 0) + 1 INTO NEW.version
    FROM graph.facet_history h
    WHERE h.entity_id = NEW.entity_id
      AND h.namespace = NEW.namespace
      AND h.prov_source_id = NEW.prov_source_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER facet_version_from_history
    BEFORE INSERT ON graph.facet
    FOR EACH ROW EXECUTE FUNCTION graph.facet_version_from_history();

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS facet_version_from_history ON graph.facet;
DROP FUNCTION IF EXISTS graph.facet_version_from_history();
-- +goose StatementEnd
