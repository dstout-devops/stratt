-- +goose Up
-- OpenTofu HTTP state backend storage (charter §8 Phase 2, ADR-0016).
-- State documents are ENCRYPTED AT REST (AES-256-GCM, STRATT_STATE_KEY) —
-- the data column holds ciphertext, never plaintext JSON; the platform can
-- serve state without being able to leak it via a stray SELECT (§2.5
-- spirit). Locks are tofu's own lock-info documents (no secret content).
CREATE TABLE graph.opentofu_state (
    workspace  text PRIMARY KEY,
    data       bytea NOT NULL,
    lock       jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE graph.opentofu_state;
