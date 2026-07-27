-- +goose Up
-- HOW a provider's verdict was reached (ADR-0138 D5), because two verdicts that both read
-- verified=true are not equally strong and the surface must not pretend otherwise (§1.8).
--
--   'manifest'    — the plugin's RUNNING binary was dialed and its own advertisement checked
--                   against the operator's `provides`. Two independent artifacts agreeing.
--   'declaration' — dial-less: there is NO Manifest to fetch. An EE-Job Actuator has no dial
--                   address by construction (ansible is subprocess-only, §3 GPLv3 boundary), so
--                   the claim is corroborated against the DECLARED mechanisms instead — the
--                   per-kind Workflow maps and dispatchable Actions, which the estate loader
--                   already validates (a provisions/decommissions entry must name a Workflow
--                   that exists). Weaker, and labelled.
--   ''            — not verified; `reason` says why.
--
-- WHY THIS EXISTS. ADR-0135 D3 shipped a capability-typed remediation route whose own flagship
-- provider could never satisfy it: verification meant fetching a Manifest over a dial address, so
-- `configmgmt` — the class whose first provider is a subprocess BY CHARTER — was structurally
-- unroutable. Three Assignments broke on every real floor while every unit test passed, because
-- the tests resolve through a fake. A capability system that structurally excludes subprocess
-- tools cannot express the one class it was most needed for.
--
-- The attestation is NOT self-certifying: the mechanism it checks lives in a different part of the
-- tree from the claim, and naming a Workflow that does not exist is already refused at load. It is
-- also honestly weaker in a second way — an ACTION-shaped class needs a Manifest-advertised
-- `implements` (ADR-0140 D1), which a dial-less provider cannot supply, so such a provider attests
-- and then fails CLOSED at resolution. Attestation admits the Workflow-shaped classes, which is
-- exactly what `configmgmt` and `provisioning` are.

ALTER TABLE graph.capability_provider
    ADD COLUMN basis text NOT NULL DEFAULT ''
        CHECK (basis IN ('', 'manifest', 'declaration'));

-- Existing verified rows were all manifest-verified: before this change a dial-less provider could
-- not reach verified=true at all. Backfilling them as 'manifest' states what was already true,
-- rather than leaving a live floor's providers looking basis-less after an upgrade.
UPDATE graph.capability_provider SET basis = 'manifest' WHERE verified;

-- +goose Down
ALTER TABLE graph.capability_provider DROP COLUMN basis;
