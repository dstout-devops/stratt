-- +goose Up
-- The one audit stream (charter §1.6): a born-here, append-only, ordered,
-- Principal-stamped ledger of who-did-what-when, hash-chained for tamper-
-- evidence (ADR-0034). Lives in the `audit` schema — born-here operational
-- records, NOT a graph projection (§1.2: the graph stays projection+provenance;
-- audit stamps ACTIONS, not attribute writes). Precedent: audit.mcp_call.
CREATE TABLE audit.event (
    seq            bigserial PRIMARY KEY,
    at             timestamptz NOT NULL DEFAULT now(),
    principal_id   text NOT NULL DEFAULT '',
    principal_kind text NOT NULL DEFAULT '',
    action         text NOT NULL,
    object         text NOT NULL DEFAULT '',
    outcome        text NOT NULL DEFAULT '',
    detail         jsonb,
    -- Hash chain (§1.8 tamper-evidence): NULL until the sealer chains the row.
    prev_hash      bytea,
    hash           bytea
);

CREATE INDEX event_principal ON audit.event (principal_id, seq);
CREATE INDEX event_action ON audit.event (action, seq);
-- The sealer's work-list: the unsealed tail, in order.
CREATE INDEX event_unsealed ON audit.event (seq) WHERE hash IS NULL;

-- Append-only + seal-once, enforced structurally (not by convention): DELETE is
-- refused, and a row may be UPDATEd only to seal it (hash NULL -> value); a
-- sealed row's content is immutable. Tampering then requires disabling this
-- trigger with elevated DB rights — which the hash chain still detects.
-- +goose StatementBegin
CREATE FUNCTION audit.event_immutable() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit.event is append-only (seq %)', OLD.seq;
    END IF;
    IF OLD.hash IS NOT NULL THEN
        RAISE EXCEPTION 'audit.event seq % is sealed and immutable', OLD.seq;
    END IF;
    IF NEW.seq <> OLD.seq OR NEW.at <> OLD.at OR NEW.principal_id <> OLD.principal_id
        OR NEW.principal_kind <> OLD.principal_kind OR NEW.action <> OLD.action
        OR NEW.object <> OLD.object OR NEW.outcome <> OLD.outcome
        OR NEW.detail IS DISTINCT FROM OLD.detail THEN
        RAISE EXCEPTION 'audit.event seq % content is immutable; only sealing may update', OLD.seq;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER event_immutable BEFORE UPDATE OR DELETE ON audit.event
    FOR EACH ROW EXECUTE FUNCTION audit.event_immutable();

-- Single-row high-water mark of the sealed prefix: the hash chain covers the
-- contiguous range [1, seq] and `hash` is the head hash the next seal extends.
CREATE TABLE audit.seal_head (
    id   boolean PRIMARY KEY DEFAULT true CHECK (id),
    seq  bigint NOT NULL DEFAULT 0,
    hash bytea
);
INSERT INTO audit.seal_head (id, seq) VALUES (true, 0);

-- Durable per-Sink forward cursor (at-least-once SIEM egress, ADR-0034): the
-- forwarder commits `through_seq` only after the SIEM acks.
CREATE TABLE audit.forward_offset (
    sink        text PRIMARY KEY,
    through_seq bigint NOT NULL DEFAULT 0,
    at          timestamptz NOT NULL DEFAULT now()
);

-- Egress delivery status (§1.8 — observability of the forwarder itself). Never
-- carries event bodies or secret material.
CREATE TABLE audit.forward_delivery (
    id          bigserial PRIMARY KEY,
    sink        text NOT NULL,
    through_seq bigint NOT NULL,
    count       integer NOT NULL DEFAULT 0,
    status      text NOT NULL,
    detail      text NOT NULL DEFAULT '',
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX forward_delivery_sink ON audit.forward_delivery (sink, at DESC);

-- +goose Down
DROP TABLE audit.forward_delivery;
DROP TABLE audit.forward_offset;
DROP TABLE audit.seal_head;
DROP TRIGGER event_immutable ON audit.event;
DROP FUNCTION audit.event_immutable();
DROP TABLE audit.event;
