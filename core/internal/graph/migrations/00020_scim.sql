-- +goose Up
-- SCIM Service Provider identity registry (charter §8, ADR-0035): a born-here
-- projection of the customer's IdP (Okta/Entra/Zitadel), which pushes Users and
-- Groups over SCIM 2.0. Lives in its OWN `scim` schema — identity is the actor
-- model (§2.5), NOT the estate graph (§1.2: graph.* stays projection+provenance
-- of the estate; Principals are actors on it, not Entities). Mirrors the
-- `audit`-schema call (born-here operational records outside graph.*). The IdP
-- stays authoritative; this registry is rebuildable by re-push.
CREATE SCHEMA IF NOT EXISTS scim;

-- scim.idp: the CaC IdP-registration config — which IdPs may push, the sha256 of
-- their bearer token (§2.5, material never stored), and their group→team
-- mappings. Written ONLY by the desired-state engine (sole writer, like
-- graph.emitter). The projection tables below are written ONLY by the SCIM
-- handler — two clearly-separated owners in one schema.
CREATE TABLE scim.idp (
    name       text PRIMARY KEY,
    token_hash text NOT NULL,
    mappings   jsonb NOT NULL DEFAULT '[]'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- scim.identity: one provisioned User per (idp, scim_id). `principal_id` is the
-- value we expect the OIDC `sub` to carry (populated from SCIM externalId, with a
-- documented fallback to userName); it is the join key to the one Principal model
-- at request time. `active` is the deactivation flag (SCIM `active:false`, the
-- primary offboarding signal); DELETE tombstones via `deleted_at`.
CREATE TABLE scim.identity (
    idp          text NOT NULL,
    scim_id      text NOT NULL,
    user_name    text NOT NULL DEFAULT '',
    external_id  text NOT NULL DEFAULT '',
    principal_id text NOT NULL DEFAULT '',
    active       boolean NOT NULL DEFAULT true,
    emails       jsonb,
    raw          jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    PRIMARY KEY (idp, scim_id)
);
-- The request-time deactivation gate looks up a live record by principal_id.
CREATE INDEX identity_principal ON scim.identity (principal_id) WHERE deleted_at IS NULL;
-- Okta/Entra reconcile by `filter=userName eq "…"` / `externalId eq …`.
CREATE INDEX identity_user_name ON scim.identity (idp, user_name) WHERE deleted_at IS NULL;
CREATE INDEX identity_external ON scim.identity (idp, external_id) WHERE deleted_at IS NULL;

-- scim.group: one provisioned Group per (idp, scim_id).
CREATE TABLE scim.group (
    idp          text NOT NULL,
    scim_id      text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    external_id  text NOT NULL DEFAULT '',
    raw          jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    PRIMARY KEY (idp, scim_id)
);
CREATE INDEX group_display_name ON scim.group (idp, display_name) WHERE deleted_at IS NULL;

-- scim.group_member: the projected membership edges (group ← member). Feeds the
-- authz tuple union as team:<mapped>#member (ADR-0035); the reconcile join to
-- scim.identity resolves each member to its principal_id and active-state.
CREATE TABLE scim.group_member (
    idp             text NOT NULL,
    group_scim_id   text NOT NULL,
    member_scim_id  text NOT NULL,
    PRIMARY KEY (idp, group_scim_id, member_scim_id)
);
CREATE INDEX group_member_member ON scim.group_member (idp, member_scim_id);

-- +goose Down
DROP TABLE scim.group_member;
DROP TABLE scim.group;
DROP TABLE scim.identity;
DROP TABLE scim.idp;
DROP SCHEMA scim;
