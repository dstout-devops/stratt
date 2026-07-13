-- +goose Up
-- Notifications (charter §8 Phase-2 "notifications"; ADR-0027): the outbound
-- mirror of the Emitter/Trigger ingest path. These are DELIVERY-PLANE infra,
-- not core-model Named Kinds (§2) — hence the notify_ prefix, mirroring the
-- awx_ compat-shim prefix. CaC-declared, sole writer is the desired-state
-- engine (like Emitter/Trigger/Baseline). No secret material lives here: a
-- Sink binds a CredentialRef by name; the url/token are injected into the
-- delivery pod at spawn (§2.5).

CREATE TABLE graph.notify_sink (
    name       text PRIMARY KEY,
    kind       text NOT NULL CHECK (kind IN ('webhook')),
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER notify_sink_touch_updated_at
    BEFORE UPDATE ON graph.notify_sink
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

CREATE TABLE graph.notify_subscription (
    name       text PRIMARY KEY,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER notify_subscription_touch_updated_at
    BEFORE UPDATE ON graph.notify_subscription
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- The delivery status surface (§1.8: a failed notification must never be
-- silent). One row per delivery attempt — a queryable product surface,
-- readable like Findings; not a second source of truth (the Notice itself is
-- transient). detail never holds secret material.
CREATE TABLE graph.notify_delivery (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    notice_kind  text NOT NULL,
    subject      text NOT NULL,
    subscription text NOT NULL,
    sink         text NOT NULL,
    status       text NOT NULL CHECK (status IN ('delivered', 'failed')),
    detail       text,
    at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notify_delivery_at_idx ON graph.notify_delivery (at DESC);
CREATE INDEX notify_delivery_sub_idx ON graph.notify_delivery (subscription, at DESC);

-- +goose Down
DROP TABLE graph.notify_delivery;
DROP TABLE graph.notify_subscription;
DROP TABLE graph.notify_sink;
