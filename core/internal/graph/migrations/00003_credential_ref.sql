-- +goose Up
-- CredentialRefs (charter §2.5, ADR-0009): pointer + injection policy to
-- brokered secret material. By construction there is NO column that can hold
-- material — the platform stores where a secret lives and how it projects
-- into execution pods, never what it is.
CREATE TABLE graph.credential_ref (
    name        text PRIMARY KEY,
    -- Ownership is always a team (no user-private credentials, ADR-0009).
    owner_team  text NOT NULL,
    backend     text NOT NULL CHECK (backend IN ('k8s-secret', 'vault', 'workload-identity')),
    -- Backend-shaped address of the material (k8s-secret: namespace/name).
    locator     jsonb NOT NULL,
    -- Per-field projection policy: [{key, as: env|file, name}].
    injection   jsonb NOT NULL,
    -- Declaration ownership, mirroring graph.view (§1.2).
    declared_by text NOT NULL DEFAULT 'api' CHECK (declared_by IN ('api', 'cac')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER credential_ref_touch_updated_at
    BEFORE UPDATE ON graph.credential_ref
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

-- +goose Down
DROP TABLE graph.credential_ref;
