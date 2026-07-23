-- +goose Up
-- Capability-provider verification (ADR-0104 D1 / follow-up #2): the runtime-derived
-- projection recording, per declared provider (a Connector or Actuator with a non-empty
-- `provides`), whether the plugin's dialed Manifest ACTUALLY advertises the capability
-- classes it was declared to provide (§1.5 "the Manifest is advertisement; the grant is
-- truth" — but a declared `provides` the plugin does not back is a PHANTOM provider).
--
-- Unlike graph.connector/graph.actuator (CaC-only, desired-state engine is sole writer),
-- this is a RUNTIME projection: the connectorregistry's leader-only verification reconcile
-- is its sole writer. It is store-visible so resolution counts only VERIFIED providers
-- IDENTICALLY on every replica (the D3 property: an every-replica Actuator consumer must
-- resolve a leader-only Connector provider the same everywhere) — never from per-replica
-- dial state. A phantom provider (verified=false) does NOT count toward any consumer's
-- satisfaction, and its reason is queryable (§1.8), never a Run-time surprise.

CREATE TABLE graph.capability_provider (
    provider_kind text NOT NULL CHECK (provider_kind IN ('connector', 'actuator')),
    provider_name text NOT NULL,
    -- True iff the dialed Manifest advertised every capability class the provider declares.
    verified      boolean NOT NULL,
    -- The phantom/dial reason when verified=false ('' when verified).
    reason        text NOT NULL DEFAULT '',
    checked_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_kind, provider_name)
);

-- +goose Down
DROP TABLE graph.capability_provider;
