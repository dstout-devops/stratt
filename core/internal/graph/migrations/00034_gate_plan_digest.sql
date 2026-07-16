-- +goose Up
-- The Gate's approved plan digest (ADR-0047 §8, Actuator slice 3d). A plan-pinned
-- Apply is verified against the exact sha256 the approver saw; binding it into the
-- Gate row makes approve-what-you-see durable (§1.8) and is the core-held state the
-- Apply boundary reads. WRITE-ONCE by construction: CreateGate sets it only on
-- INSERT and never in the ON CONFLICT DO UPDATE, so a re-plan can never silently
-- rebind a different digest under an approval. '' for a non-plan Gate.
ALTER TABLE graph.gate ADD COLUMN plan_digest text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE graph.gate DROP COLUMN plan_digest;
