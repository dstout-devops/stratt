-- +goose Up
-- Contract pin registry (charter §1.5, ADR-0015): the public, auditable
-- record of which schema document (by content hash) each Contract
-- name+version is pinned to. The documents themselves are data in the
-- contracts/ module; validation uses the embedded copies — this table is the
-- drift tripwire: a registered pin that stops matching the shipped document
-- blocks startup.
CREATE TABLE graph.contract (
    name          text NOT NULL,
    version       int  NOT NULL,
    -- Derivation-ladder provenance of the schema itself (§2.2):
    -- hand-written | tool-derived | mcp-declared.
    rung          text NOT NULL CHECK (rung IN ('hand-written', 'tool-derived', 'mcp-declared')),
    hash          text NOT NULL,
    schema        jsonb NOT NULL,
    registered_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (name, version)
);

-- +goose Down
DROP TABLE graph.contract;
