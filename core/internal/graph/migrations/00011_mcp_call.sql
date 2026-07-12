-- +goose Up
-- Per-identity usage accounting for the platform MCP server (charter §1.6:
-- "cost/usage accounting per identity"; ADR-0021). One row per tool
-- invocation; Phase-4 per-Principal cost analytics builds on this record.
-- Lives in its own schema: graph stays provably projection + provenance
-- (§1.2); audit holds born-here operational records (guardian on ADR-0021).
CREATE SCHEMA IF NOT EXISTS audit;
CREATE TABLE audit.mcp_call (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    principal      text NOT NULL,
    principal_kind text NOT NULL DEFAULT '',
    tool           text NOT NULL,
    ok             boolean NOT NULL,
    duration_ms    bigint NOT NULL DEFAULT 0,
    at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mcp_call_principal_tool ON audit.mcp_call (principal, tool);

-- +goose Down
DROP TABLE audit.mcp_call;
DROP SCHEMA IF EXISTS audit;
