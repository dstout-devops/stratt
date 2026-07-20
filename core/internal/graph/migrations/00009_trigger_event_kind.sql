-- +goose Up
-- Trigger kind `event` (ADR-0018) joins schedule (ADR-0010).
-- expand/contract-ok: widens a CHECK constraint (drops then re-adds a broader
-- one) — backward-compatible; the previous release's writers only produce values
-- still valid under the wider check. Pre-dates UPG-1/ADR-0078; grandfathered.
ALTER TABLE graph.trigger DROP CONSTRAINT trigger_kind_check;
ALTER TABLE graph.trigger ADD CONSTRAINT trigger_kind_check
    CHECK (kind IN ('schedule', 'event'));

-- +goose Down
ALTER TABLE graph.trigger DROP CONSTRAINT trigger_kind_check;
ALTER TABLE graph.trigger ADD CONSTRAINT trigger_kind_check
    CHECK (kind IN ('schedule'));
