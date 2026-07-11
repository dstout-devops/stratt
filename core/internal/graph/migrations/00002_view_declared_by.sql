-- +goose Up
-- Declaration ownership for Views (charter §1.2 / §2.1): desired state lives
-- in Git. 'cac' marks a View owned by the declared desired state (the Git
-- sync controller / stratt apply); 'api' marks a directly-declared View.
-- CaC may adopt an api View (promotion into Git); the api path may never
-- modify a cac View — enforced by the WHERE guards in the store, and the
-- CHECK keeps the vocabulary closed.
ALTER TABLE graph.view
    ADD COLUMN declared_by text NOT NULL DEFAULT 'api'
    CHECK (declared_by IN ('api', 'cac'));

-- A View's version tracks its *selector content* (§2.1), not row churn:
-- adoption (declared_by flips, selector unchanged) must not mint a version.
-- Bump only on selector change; record history only when a version was
-- minted (an unconditional AFTER UPDATE insert would collide on the
-- (name, version) history PK for adoption-only updates).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.view_bump_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.selector IS DISTINCT FROM OLD.selector THEN
        NEW.version := OLD.version + 1;
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION graph.view_record_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.version = OLD.version THEN
        RETURN NEW; -- no new selector version minted (e.g. adoption)
    END IF;
    INSERT INTO graph.view_history (name, version, selector)
    VALUES (NEW.name, NEW.version, NEW.selector);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE graph.view DROP COLUMN declared_by;
