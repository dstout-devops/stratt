-- +goose Up
-- Per-key Entity-LABEL ownership (charter §2.1/§2.4, ADR-0041): the label
-- equivalent of graph.facet_owner. Two Sources correlating onto one Entity no
-- longer clobber each other's labels — the Projector now merges labels per-key
-- (labels || $incoming) and this trigger enforces that a Syncer may only write
-- label keys it owns. Keys are source-scoped (graph.name / vcenter.name /
-- aws.* / cert.commonName); run-provenance writes bypass (stratt.workspace,
-- createvm), exactly like facets (§4.3 flap damping).
CREATE TABLE graph.label_owner (
    key        text PRIMARY KEY,
    owner_kind text NOT NULL CHECK (owner_kind IN ('syncer', 'blueprint', 'team')),
    owner_ref  text NOT NULL,
    -- view_scope is stored but not yet trigger-enforced — a symmetric deferral
    -- with facet_owner (see 00001_graph_spine.sql). All current owners are
    -- unscoped (NULL); narrow enforcement to the View when a scoped owner ships.
    view_scope text
);

-- +goose StatementBegin
CREATE FUNCTION graph.enforce_label_owner() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    old_labels jsonb := '{}'::jsonb;
    k text;
    o graph.label_owner%ROWTYPE;
BEGIN
    -- Run/blueprint-provenance writes bypass (like facets, §4.3); only Syncer
    -- label writes are ownership-checked.
    IF NEW.prov_writer_kind <> 'syncer' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        old_labels := OLD.labels;
    END IF;
    -- Every label key this write ADDS or CHANGES vs the prior bag must be owned
    -- by this Syncer (unchanged keys belong to other owners and are preserved).
    FOR k IN
        SELECT je.key FROM jsonb_each(NEW.labels) je
        WHERE (old_labels -> je.key) IS DISTINCT FROM je.value
    LOOP
        SELECT * INTO o FROM graph.label_owner WHERE key = k;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING errcode = 'P0001',
                message = format('label key %s has no registered owner (charter §2.1: registration precedes writes)', k);
        END IF;
        IF o.owner_kind <> 'syncer' OR o.owner_ref <> NEW.prov_writer_ref THEN
            RAISE EXCEPTION USING errcode = 'P0001',
                message = format('syncer %s may not write label key %s: owned by %s %s (charter §2.4: no cross-source precedence)', NEW.prov_writer_ref, k, o.owner_kind, o.owner_ref);
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER label_owner_check
    BEFORE INSERT OR UPDATE ON graph.entity
    FOR EACH ROW EXECUTE FUNCTION graph.enforce_label_owner();

-- +goose Down
DROP TRIGGER label_owner_check ON graph.entity;
DROP FUNCTION graph.enforce_label_owner();
DROP TABLE graph.label_owner;
