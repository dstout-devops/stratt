-- Run outputs (charter §2.2/§2.3, ADR-0031): an Action produces typed output
-- values validated against its output Contract. Postgres stores the summary
-- only (§3) — outputs are a small typed document (serials, ids), not a fact
-- stream. Nullable: Actuator Runs and Actions with no outputs leave it NULL.
-- +goose Up
ALTER TABLE graph.run ADD COLUMN outputs jsonb;

-- +goose Down
ALTER TABLE graph.run DROP COLUMN outputs;
