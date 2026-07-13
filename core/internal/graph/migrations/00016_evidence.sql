-- +goose Up
-- Evidence (charter §2.4, ADR-0029): the manifest pointing at a Finding's
-- sealed, object-locked audit bundle. The immutable bundle lives in the object
-- store; this row is the graph's rebuildable POINTER to it (a projection, not a
-- second copy — §1.2). sha256 is the tamper-evidence anchor. One live Evidence
-- per Finding (the unique index), so re-sealing is a no-op (write-once).
CREATE TABLE graph.evidence (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    finding_id   uuid NOT NULL REFERENCES graph.finding(id) ON DELETE CASCADE,
    baseline     text NOT NULL,
    target       text NOT NULL,
    object_key   text NOT NULL,
    sha256       text NOT NULL,
    size_bytes   bigint NOT NULL,
    sealed_at    timestamptz NOT NULL DEFAULT now(),
    retain_until timestamptz NOT NULL
);

-- One Evidence bundle per Finding — the write-once guarantee at the graph layer.
CREATE UNIQUE INDEX evidence_finding_unique ON graph.evidence (finding_id);

-- +goose Down
DROP TABLE graph.evidence;
