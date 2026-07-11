-- 00001_graph_spine.sql — the Phase-0 graph plane (charter §2.1, §3, §8).
--
-- The graph is a rebuildable projection, never a second truth (§1.2). That is
-- enforced HERE, in the data layer, two ways:
--
--   1. Write-path gate: the projection tables (entity, entity_identity,
--      relation, facet) reject any write unless the transaction has declared
--      itself one of the two legal write paths — a Normalizer or Run
--      provenance — via `SET LOCAL stratt.write_path`. Application code that
--      "just inserts" fails loudly.
--   2. Facet ownership registry: every Facet namespace has exactly one
--      declared owner (PRIMARY KEY makes a double-claim structurally
--      impossible, §2.1). A Syncer write to a namespace it does not own is
--      rejected. Run-provenance writes are legal to any registered namespace:
--      §4.3 explicitly lets a Run write Facets directly (with Run provenance)
--      ahead of Syncer lag — provenance is a lineage, never a fight.
--
-- Provenance is non-optional (§2.1): NOT NULL + CHECK on every projected row.
-- Facets are versioned (§3): every write appends to facet_history.
-- Runs store summaries only (§3): events stream on NATS, artifacts in S3 —
-- the AWX job-events-table pathology is structurally absent.
--
-- Recorded follow-ups (charter-guardian review, 2026-07-11 — advisory):
--   * Before lower-trust code can reach a pooled connection (community-tier
--     plugins, multi-tenancy), harden the write gate from a session setting
--     to a privilege boundary (writer role + SECURITY DEFINER, or RLS).
--   * facet_owner.view_scope is stored but not trigger-enforced yet; it must
--     become data-layer-enforced before View-scoped multi-party ownership
--     lands (Phase 2).
--   * When the Intent compiler lands (Phase 2), Run writes must carry a
--     Baseline/Blueprint ref checked against ownership eligibility (§2.4).
--   * Facet schema validation attaches at the Projector write path when
--     Contracts land (Phase 2), keyed by pinned, hash-verified schema refs
--     carried on facet_owner (§1.5).

-- +goose Up
CREATE SCHEMA graph;

-- ── Entities ────────────────────────────────────────────────────────────────

CREATE TABLE graph.entity (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind             text NOT NULL,
    labels           jsonb NOT NULL DEFAULT '{}'::jsonb,
    prov_writer_kind text NOT NULL CHECK (prov_writer_kind IN ('syncer', 'run')),
    prov_writer_ref  text NOT NULL,
    prov_source_id   text,
    prov_at          timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- Tombstone, set when the owning Source stops reporting the Entity. Kept
    -- (not hard-deleted) so Run per-target results stay resolvable; the
    -- projection remains rebuildable either way (§1.2).
    deleted_at       timestamptz
);

CREATE INDEX entity_kind_idx   ON graph.entity (kind) WHERE deleted_at IS NULL;
CREATE INDEX entity_labels_gin ON graph.entity USING gin (labels jsonb_path_ops);

-- External identities an Entity is known by, one row per (scheme, value).
-- UNIQUE (scheme, value) is the correlation guarantee: two Entities can never
-- claim the same external identity.
CREATE TABLE graph.entity_identity (
    entity_id uuid NOT NULL REFERENCES graph.entity (id) ON DELETE CASCADE,
    scheme    text NOT NULL,
    value     text NOT NULL,
    PRIMARY KEY (entity_id, scheme),
    UNIQUE (scheme, value)
);

CREATE INDEX entity_identity_lookup_idx ON graph.entity_identity (scheme, value);

-- ── Relations ───────────────────────────────────────────────────────────────

CREATE TABLE graph.relation (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type             text NOT NULL,
    from_id          uuid NOT NULL REFERENCES graph.entity (id) ON DELETE CASCADE,
    to_id            uuid NOT NULL REFERENCES graph.entity (id) ON DELETE CASCADE,
    prov_writer_kind text NOT NULL CHECK (prov_writer_kind IN ('syncer', 'run')),
    prov_writer_ref  text NOT NULL,
    prov_source_id   text,
    prov_at          timestamptz NOT NULL,
    UNIQUE (type, from_id, to_id)
);

CREATE INDEX relation_from_idx ON graph.relation (from_id);
CREATE INDEX relation_to_idx   ON graph.relation (to_id);

-- ── Facets (schemas attach here and nowhere else, §1.1) ─────────────────────

CREATE TABLE graph.facet (
    entity_id        uuid NOT NULL REFERENCES graph.entity (id) ON DELETE CASCADE,
    namespace        text NOT NULL,
    value            jsonb NOT NULL,
    version          bigint NOT NULL DEFAULT 1,
    prov_writer_kind text NOT NULL CHECK (prov_writer_kind IN ('syncer', 'run')),
    prov_writer_ref  text NOT NULL,
    prov_source_id   text,
    prov_at          timestamptz NOT NULL,
    PRIMARY KEY (entity_id, namespace)
);

CREATE INDEX facet_namespace_idx ON graph.facet (namespace);
CREATE INDEX facet_value_gin     ON graph.facet USING gin (value jsonb_path_ops);

-- Append-only version history (§3 "JSONB, versioned"): one row per write,
-- populated by trigger — no code path can skip it.
CREATE TABLE graph.facet_history (
    entity_id        uuid NOT NULL,
    namespace        text NOT NULL,
    version          bigint NOT NULL,
    value            jsonb NOT NULL,
    prov_writer_kind text NOT NULL,
    prov_writer_ref  text NOT NULL,
    prov_source_id   text,
    prov_at          timestamptz NOT NULL,
    recorded_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_id, namespace, version)
);

-- ── Facet ownership registry (§2.1) ─────────────────────────────────────────
-- PRIMARY KEY (namespace): a second registration for the same namespace is a
-- constraint violation — a registration error by construction, never a
-- precedence fight. view_scope narrows where the owner may write (enforced at
-- the application registration layer in Phase 0).

CREATE TABLE graph.facet_owner (
    namespace  text PRIMARY KEY,
    owner_kind text NOT NULL CHECK (owner_kind IN ('syncer', 'blueprint', 'team')),
    owner_ref  text NOT NULL,
    view_scope text
);

-- ── Sources & sync bookkeeping (§2.2) ───────────────────────────────────────

CREATE TABLE graph.source (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text NOT NULL,
    name           text NOT NULL UNIQUE,
    endpoint       text NOT NULL,
    -- Pointer only (§2.5): secret material never persists in the platform.
    credential_ref text,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE graph.source_sync (
    source_id        uuid PRIMARY KEY REFERENCES graph.source (id) ON DELETE CASCADE,
    -- Opaque delta cursor (e.g. vSphere PropertyCollector version string).
    cursor           text,
    last_full_sync_at timestamptz,
    last_delta_at     timestamptz
);

-- ── Views (§2.1: saved, versioned, CaC-declared graph queries) ──────────────

CREATE TABLE graph.view (
    name       text PRIMARY KEY,
    version    bigint NOT NULL DEFAULT 1,
    selector   jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE graph.view_history (
    name        text NOT NULL,
    version     bigint NOT NULL,
    selector    jsonb NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (name, version)
);

-- ── Runs (§2.3 — summaries only, §3) ────────────────────────────────────────

CREATE TABLE graph.run (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id  text NOT NULL,
    status       text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    view_ref     text,
    view_version bigint,
    -- Aggregate summary (per-target counts, artifact refs). Never the event
    -- stream: that lives on NATS/S3 (§3).
    summary      jsonb NOT NULL DEFAULT '{}'::jsonb,
    started_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);

CREATE INDEX run_status_idx ON graph.run (status, started_at DESC);

-- ── Data-layer write enforcement (§1.2) ─────────────────────────────────────

-- +goose StatementBegin
CREATE FUNCTION graph.enforce_write_path() RETURNS trigger
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

CREATE TRIGGER entity_write_path
    BEFORE INSERT OR UPDATE OR DELETE ON graph.entity
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_write_path();
CREATE TRIGGER entity_identity_write_path
    BEFORE INSERT OR UPDATE OR DELETE ON graph.entity_identity
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_write_path();
CREATE TRIGGER relation_write_path
    BEFORE INSERT OR UPDATE OR DELETE ON graph.relation
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_write_path();
CREATE TRIGGER facet_write_path
    BEFORE INSERT OR UPDATE OR DELETE ON graph.facet
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_write_path();

-- +goose StatementBegin
CREATE FUNCTION graph.enforce_facet_owner() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    o graph.facet_owner%ROWTYPE;
BEGIN
    SELECT * INTO o FROM graph.facet_owner WHERE namespace = NEW.namespace;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING
            errcode = 'P0001',
            message = format(
                'facet namespace %s has no registered owner (charter §2.1: registration precedes writes)',
                NEW.namespace);
    END IF;
    -- Run-provenance writes are always admissible to a registered namespace
    -- (§4.3 flap damping: the Run writes ahead of Syncer lag). Syncer writes
    -- must come from the declared owner.
    IF NEW.prov_writer_kind = 'syncer'
       AND (o.owner_kind <> 'syncer' OR o.owner_ref <> NEW.prov_writer_ref) THEN
        RAISE EXCEPTION USING
            errcode = 'P0001',
            message = format(
                'syncer %s may not write facet namespace %s: owned by %s %s (charter §2.1: provenance is a lineage, never a fight)',
                NEW.prov_writer_ref, NEW.namespace, o.owner_kind, o.owner_ref);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER facet_owner_check
    BEFORE INSERT OR UPDATE ON graph.facet
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_facet_owner();

-- +goose StatementBegin
CREATE FUNCTION graph.facet_versioning() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        NEW.version := OLD.version + 1;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER facet_versioning
    BEFORE UPDATE ON graph.facet
    FOR EACH ROW EXECUTE FUNCTION graph.facet_versioning();

-- +goose StatementBegin
CREATE FUNCTION graph.facet_record_history() RETURNS trigger
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
-- +goose StatementEnd

CREATE TRIGGER facet_record_history
    AFTER INSERT OR UPDATE ON graph.facet
    FOR EACH ROW EXECUTE FUNCTION graph.facet_record_history();

-- Version bump runs BEFORE UPDATE (it rewrites NEW); history recording runs
-- AFTER so it only fires for rows actually written — a BEFORE INSERT trigger
-- would also fire on the insert arm of ON CONFLICT DO UPDATE upserts and
-- record history for a row that was never inserted.
-- +goose StatementBegin
CREATE FUNCTION graph.view_bump_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := OLD.version + 1;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER view_bump_version
    BEFORE UPDATE ON graph.view
    FOR EACH ROW EXECUTE FUNCTION graph.view_bump_version();

-- +goose StatementBegin
CREATE FUNCTION graph.view_record_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO graph.view_history (name, version, selector)
    VALUES (NEW.name, NEW.version, NEW.selector);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER view_record_history
    AFTER INSERT OR UPDATE ON graph.view
    FOR EACH ROW EXECUTE FUNCTION graph.view_record_history();

-- +goose StatementBegin
CREATE FUNCTION graph.touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER entity_touch_updated_at
    BEFORE UPDATE ON graph.entity
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- +goose Down
DROP SCHEMA graph CASCADE;
