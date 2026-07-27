-- +goose Up
-- The class→Action mapping a provider ADVERTISES (ADR-0140 D1), recorded alongside its
-- verification verdict by the same leader-only reconcile that writes the rest of this row.
--
-- It is stored rather than dialed-for because resolution must be HEALTH-INDEPENDENT and
-- REPLICA-CONSISTENT — the same two properties `verified` exists for (00039). Resolving a
-- capability by dialing the provider would make a Run's routing depend on whether this
-- replica could reach the plugin in this instant, which is precedence-by-liveness (§2.4)
-- and exactly the hazard the verification projection was introduced to remove.
--
-- Before this, core DERIVED the mapping: `<plugin_identity>/<class>-resolve`, concatenated
-- in Go. That made the spine dictate names inside a namespace it does not own — a provider
-- whose Action was named anything else could not serve the class, whatever its Manifest
-- said. The class exists to make the provider swappable; computing the name constrained the
-- provider's internals instead. Now the provider declares it and core carries the token
-- opaquely, like every other class→mechanism mapping (`provisions`/`remediates`/
-- `decommissions`, ADR-0140 D2).
--
-- SHAPE: {"<capability class>": "<Action name>"}. Restricted to classes the operator GRANTED
-- via `provides` — an advertisement for an ungranted class is ignored, never recorded (§1.5:
-- the Manifest advertises, the grant is truth).
--
-- EMPTY IS LAWFUL and is the common case. Only classes reached through a resolve Action
-- appear here (ipam, statestore). A class whose consumers route through a per-kind Workflow
-- map — `provisioning` via `provisions: {Compute: …}` — has no resolve Action at all, so its
-- providers map to nothing and stay fully verified. Requiring an entry per declared `provides`
-- would phantom every provisioning provider in the estate (ADR-0140 D3: three Step shapes,
-- three resolutions).

ALTER TABLE graph.capability_provider
    ADD COLUMN implements jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE graph.capability_provider DROP COLUMN implements;
