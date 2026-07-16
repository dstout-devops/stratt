-- +goose Up
-- Content-addressed, encrypted, immutable saved-plan artifacts (ADR-0047 §8,
-- Actuator slice 3). A saved actuator plan (a tofu plan) is secret-bearing, so it
-- is NOT stored in evidencestore (WORM/compliance-locked plaintext, and a Named
-- Kind meaning a Finding's backing). It lives here keyed by the sha256 of its
-- PLAINTEXT — the digest a Gate binds and a human approves (§1.8). `data` is
-- AES-256-GCM ciphertext; encryption lives in the planstore package and this
-- table never sees plaintext (§2.5). WRITE-ONCE: the primary key + ON CONFLICT DO
-- NOTHING mean a fixed digest can never be re-pointed at different bytes — the
-- TOCTOU anchor the Apply boundary re-verifies against.
CREATE TABLE graph.plan_artifact (
    sha256     text PRIMARY KEY,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE graph.plan_artifact;
