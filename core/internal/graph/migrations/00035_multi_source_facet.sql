-- ADR-0060: multi-source Facet projection. The Facet grain gains a SOURCE
-- dimension so MANY sources may project one namespace, each retaining its own
-- provenance-stamped row — never a global per-namespace monopoly that strips
-- capability from plugins (a plugin projects everything its system reports). The
-- §1.2 single-writer invariant now binds (Entity, namespace, source) — stronger
-- than the old estate-wide lock (which added zero per-Entity protection). The
-- effective "truth" is the source declared authoritative in sources/ CaC (ADR-0056);
-- undeclared contention surfaces a Finding (framework 'ownership'), never a silent
-- pick. See docs/adr/0060-multi-source-facet-ownership.md.

-- +goose Up
-- +goose StatementBegin
-- M7 backfill: existing run-provenance Facet rows carry prov_source_id NULL; a NULL
-- cannot enter the new key. Assign the empty-string source ('' — the reserved
-- no-Source key: NOT NULL, and skipped by the home-gate uuid cast `sid <> ''`).
-- The graph is a rebuildable projection; a genuine per-Actuator source is the M2
-- follow-up. The §1.2 facet_write_path guard would reject this backfill (it guards
-- EVERY graph.facet write), so disable graph.facet's USER triggers for the backfill
-- only, then re-enable — the invariant is unchanged, this is a one-shot schema move.
ALTER TABLE graph.facet DISABLE TRIGGER USER;
UPDATE graph.facet         SET prov_source_id = '' WHERE prov_source_id IS NULL;
ALTER TABLE graph.facet ENABLE TRIGGER USER;
UPDATE graph.facet_history SET prov_source_id = '' WHERE prov_source_id IS NULL;

ALTER TABLE graph.facet         ALTER COLUMN prov_source_id SET NOT NULL;
ALTER TABLE graph.facet_history ALTER COLUMN prov_source_id SET NOT NULL;

-- Re-key the Facet + its history to include the source (M1, M6). Single-writer is
-- now the PK itself — decided under the unique index, no secondary SELECT (no TOCTOU).
ALTER TABLE graph.facet         DROP CONSTRAINT facet_pkey;
ALTER TABLE graph.facet         ADD  CONSTRAINT facet_pkey PRIMARY KEY (entity_id, namespace, prov_source_id);
ALTER TABLE graph.facet_history DROP CONSTRAINT facet_history_pkey;
ALTER TABLE graph.facet_history ADD  CONSTRAINT facet_history_pkey PRIMARY KEY (entity_id, namespace, prov_source_id, version);

-- Facet ownership registry: many sources may own a namespace, keyed per
-- (namespace, owner_ref) — the estate-wide per-namespace lock is dropped.
-- Registration still gates who MAY write (§2.5); the AUTHORITATIVE view is a
-- separate sources/ CaC declaration, never overloaded into this table.
ALTER TABLE graph.facet_owner DROP CONSTRAINT facet_owner_pkey;
ALTER TABLE graph.facet_owner ADD  CONSTRAINT facet_owner_pkey PRIMARY KEY (namespace, owner_ref);
-- +goose StatementEnd

-- +goose StatementBegin
-- Re-base the ownership guard (M3): registration-only, decided under the row PK.
-- A namespace must have >=1 registered owner. A SYNCER write must come from a
-- registered syncer-owner of that namespace (a different, unregistered syncer is
-- rejected — §2.5). A RUN write stays admissible to any registered namespace (the
-- §4.3 damp path; gating runs by their own registered source — dropping this
-- bypass — is the M3 follow-up, once run sources are registered).
CREATE OR REPLACE FUNCTION graph.enforce_facet_owner() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM graph.facet_owner WHERE namespace = NEW.namespace) THEN
        RAISE EXCEPTION USING errcode = 'P0001',
            message = format(
                'facet namespace %s has no registered owner (charter §2.1: registration precedes writes)',
                NEW.namespace);
    END IF;
    IF NEW.prov_writer_kind = 'syncer'
       AND NOT EXISTS (
           SELECT 1 FROM graph.facet_owner
           WHERE namespace = NEW.namespace
             AND owner_kind = 'syncer'
             AND owner_ref  = NEW.prov_writer_ref) THEN
        RAISE EXCEPTION USING errcode = 'P0001',
            message = format(
                'syncer %s may not write facet namespace %s: not a registered owner (charter §2.5 bounded grant)',
                NEW.prov_writer_ref, NEW.namespace);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Collapse back to one row per (Entity, namespace): keep the authoritative-ish
-- last write per pair (best-effort; the up-migration is the supported direction).
DELETE FROM graph.facet a USING graph.facet b
  WHERE a.entity_id = b.entity_id AND a.namespace = b.namespace
    AND a.prov_source_id < b.prov_source_id;
ALTER TABLE graph.facet         DROP CONSTRAINT facet_pkey;
ALTER TABLE graph.facet         ADD  CONSTRAINT facet_pkey PRIMARY KEY (entity_id, namespace);
ALTER TABLE graph.facet_history DROP CONSTRAINT facet_history_pkey;
ALTER TABLE graph.facet_history ADD  CONSTRAINT facet_history_pkey PRIMARY KEY (entity_id, namespace, version);
ALTER TABLE graph.facet_owner   DROP CONSTRAINT facet_owner_pkey;
ALTER TABLE graph.facet_owner   ADD  CONSTRAINT facet_owner_pkey PRIMARY KEY (namespace);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.enforce_facet_owner() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    o graph.facet_owner%ROWTYPE;
BEGIN
    SELECT * INTO o FROM graph.facet_owner WHERE namespace = NEW.namespace;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING errcode = 'P0001',
            message = format('facet namespace %s has no registered owner (charter §2.1: registration precedes writes)', NEW.namespace);
    END IF;
    IF NEW.prov_writer_kind = 'syncer'
       AND (o.owner_kind <> 'syncer' OR o.owner_ref <> NEW.prov_writer_ref) THEN
        RAISE EXCEPTION USING errcode = 'P0001',
            message = format('syncer %s may not write facet namespace %s: owned by %s %s', NEW.prov_writer_ref, NEW.namespace, o.owner_kind, o.owner_ref);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
