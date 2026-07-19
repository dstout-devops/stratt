-- +goose Up
-- Allow the token-less stream/poller Emitter kind (charter §2.2, ADR-0039): a
-- subscriber that outbound-connects to an external stream (e.g. the Salt event
-- bus) and publishes onto the emitter stream. It has no inbound token.
-- expand/contract-ok: widens a CHECK constraint (drop + re-add broader) —
-- backward-compatible; old writers' values stay valid. Pre-dates UPG-1; grandfathered.
ALTER TABLE graph.emitter DROP CONSTRAINT emitter_kind_check;
ALTER TABLE graph.emitter ADD CONSTRAINT emitter_kind_check
    CHECK (kind IN ('webhook', 'alertmanager', 'stream'));

-- +goose Down
ALTER TABLE graph.emitter DROP CONSTRAINT emitter_kind_check;
ALTER TABLE graph.emitter ADD CONSTRAINT emitter_kind_check
    CHECK (kind IN ('webhook', 'alertmanager'));
