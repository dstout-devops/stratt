-- ADR-0152 (EXPAND half): a Facet is identified on an Entity by (namespace, qualifier).
--
-- THE PROBLEM: one host may run exactly one Stratt-managed application. graph.facet is keyed
-- (entity_id, namespace, prov_source_id) and an exclusive claim is exclusive at (namespace,
-- entity), so apache converging app.config on web-02 and tomcat converging app.config on web-02
-- is a double-claim the compiler correctly refuses. A host running apache on :80 and tomcat on
-- :8080 is ordinary. ADR-0148 D6 recorded the limit; this is the grain change it deferred.
--
-- THE QUALIFIER IS DERIVED AT COMPILE from the resolved spec, never observed (ADR-0152 D3). A key
-- sourced from L2 is undetectable until both Runs have executed — both compile green, both
-- dispatch, and the winner is whoever bound the socket first, which is execution-order precedence
-- with no field anyone can review (§2.4).
--
-- WHY THE FACET GRAIN AND NOT JUST THE CLAIM (D4): keying only the claim leaves two Blueprints
-- compiling green and then writing the byte-identical row — upsertFacetTx does
-- ON CONFLICT (entity_id, namespace, prov_source_id) DO UPDATE and a Run write carries
-- prov_source_id = '' — so the last Run silently wins and the drift loop reports the loser as
-- drifted forever. facet_history would record the two applications as ONE interleaved version
-- chain flapping between 80 and 8080, so even the §1.8 descent could not tell them apart. §1.2 is
-- explicit that this class of invariant is enforced in the data layer, not by convention.
--
-- THIS RELEASE CANNOT YET STORE TWO QUALIFIED FACTS, and that is deliberate rather than a
-- shortfall. facet_pkey stays (entity_id, namespace, prov_source_id), so a second row differing
-- only in qualifier still violates it. 00035 re-keyed this table in ONE release only because it
-- was explicitly grandfathered as pre-dating ADR-0078; this change does not get that. The old
-- release's replicas upsert through ON CONFLICT (entity_id, namespace, prov_source_id), which
-- needs that exact unique constraint to exist — dropping it mid-rollout breaks every one of them.
-- So: expand here (the column, the widened unique indexes, every reader and writer taught), and a
-- later CONTRACT release folds the column into both primary keys. The capability arrives then.
--
-- The empty string is the ordinary case, not a sentinel for "missing": an unqualified Facet is
-- what almost every namespace is and always will be. Same convention 00035 chose for the
-- no-Source key, for the same reason — NOT NULL, and it sorts and compares without special-casing.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE graph.facet         ADD COLUMN qualifier text NOT NULL DEFAULT '';
ALTER TABLE graph.facet_history ADD COLUMN qualifier text NOT NULL DEFAULT '';

-- The keys the CONTRACT release will promote. Created now, while they are still implied by the
-- narrower primary keys (a unique (a,b,c) makes (a,b,c,d) unique too), so the fold is a promotion
-- of an index that already exists and has already been maintained rather than a build under lock.
CREATE UNIQUE INDEX facet_qualified_key
    ON graph.facet (entity_id, namespace, prov_source_id, qualifier);
CREATE UNIQUE INDEX facet_history_qualified_key
    ON graph.facet_history (entity_id, namespace, prov_source_id, qualifier, version);
-- +goose StatementEnd

-- +goose StatementBegin
-- History carries the qualifier, or a qualified row's descent loses the dimension that
-- distinguishes it — "why is this value here" would answer for the wrong application (§1.8).
-- CREATE OR REPLACE and column-defaulted, so the previous release's replicas keep inserting
-- through this same trigger and simply record the empty qualifier they already mean.
CREATE OR REPLACE FUNCTION graph.facet_record_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO graph.facet_history
        (entity_id, namespace, qualifier, version, value,
         prov_writer_kind, prov_writer_ref, prov_source_id, prov_at)
    VALUES
        (NEW.entity_id, NEW.namespace, NEW.qualifier, NEW.version, NEW.value,
         NEW.prov_writer_kind, NEW.prov_writer_ref, NEW.prov_source_id, NEW.prov_at);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- The monotonic-version sequence is PER QUALIFIER. Without this, a second application's first
-- write continues the first application's chain — or, once the contract release lands, collides
-- with it at version 1 and fails the whole sync. That collision is precisely the defect 00046 was
-- written to fix, arriving again one dimension over.
CREATE OR REPLACE FUNCTION graph.facet_version_from_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT coalesce(max(h.version), 0) + 1 INTO NEW.version
    FROM graph.facet_history h
    WHERE h.entity_id = NEW.entity_id
      AND h.namespace = NEW.namespace
      AND h.prov_source_id = NEW.prov_source_id
      AND h.qualifier = NEW.qualifier;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS graph.facet_qualified_key;
DROP INDEX IF EXISTS graph.facet_history_qualified_key;

CREATE OR REPLACE FUNCTION graph.facet_record_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO graph.facet_history
        (entity_id, namespace, version, value,
         prov_writer_kind, prov_writer_ref, prov_source_id, prov_at)
    VALUES
        (NEW.entity_id, NEW.namespace, NEW.version, NEW.value,
         NEW.prov_writer_kind, NEW.prov_writer_ref, NEW.prov_source_id, NEW.prov_at);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION graph.facet_version_from_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    SELECT coalesce(max(h.version), 0) + 1 INTO NEW.version
    FROM graph.facet_history h
    WHERE h.entity_id = NEW.entity_id
      AND h.namespace = NEW.namespace
      AND h.prov_source_id = NEW.prov_source_id;
    RETURN NEW;
END;
$$;

ALTER TABLE graph.facet         DROP COLUMN qualifier;
ALTER TABLE graph.facet_history DROP COLUMN qualifier;
-- +goose StatementEnd
