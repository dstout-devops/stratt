-- +goose Up
-- Sites — remote execution loci (charter §2.3, §3 "leaf-node Sites"; ADR-0032).
-- A Site is a CaC-declared PROJECTION, not core-model attribute state (§1.2):
-- the declaration lives in Git, the sole writer is the desired-state engine
-- (like View/Trigger/Emitter/notify_sink). It holds NO secret material and NO
-- live status — an agent's up/down heartbeat is ephemeral and lives in NATS KV,
-- never in the graph (writing it here would make the graph a second truth about
-- a fact the substrate owns). The built-in "local" locus is never a row here.

CREATE TABLE graph.site (
    name        text PRIMARY KEY CHECK (name <> 'local'),
    mode        text NOT NULL CHECK (mode IN ('push', 'pull')),
    namespace   text,
    description  text,
    declared_by text NOT NULL DEFAULT 'cac' CHECK (declared_by IN ('cac', 'api')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER site_touch_updated_at
    BEFORE UPDATE ON graph.site
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- Where a Run ran (§1.8 descent: "where did this execute"). A per-target,
-- location-routed Run can straddle loci, so this is the UNION of Sites the Run
-- touched — e.g. ["local"], ["edge-west"], or ["edge-west","local"]. Summaries
-- only; the per-event Site rides the NATS run-event stream (RunEvent.Site).
ALTER TABLE graph.run ADD COLUMN sites jsonb;

-- +goose Down
ALTER TABLE graph.run DROP COLUMN sites;
DROP TABLE graph.site;
