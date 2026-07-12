-- +goose Up
-- CaC-declared external MCP servers for the `mcp` Actuator (charter §2.3,
-- ADR-0022): the projection of the Git declaration, CaC-only like triggers/
-- workflows/emitters/baselines. The stdio server's source lives in the spec
-- verbatim — Git review authorizes exactly what the sandbox runs.
CREATE TABLE graph.mcp_server (
    name       text PRIMARY KEY,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER mcp_server_touch_updated_at
    BEFORE UPDATE ON graph.mcp_server
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- +goose Down
DROP TABLE graph.mcp_server;
