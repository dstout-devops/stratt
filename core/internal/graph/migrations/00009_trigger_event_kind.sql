-- +goose Up
-- Trigger kind `event` (ADR-0018) joins schedule (ADR-0010).
ALTER TABLE graph.trigger DROP CONSTRAINT trigger_kind_check;
ALTER TABLE graph.trigger ADD CONSTRAINT trigger_kind_check
    CHECK (kind IN ('schedule', 'event'));

-- +goose Down
ALTER TABLE graph.trigger DROP CONSTRAINT trigger_kind_check;
ALTER TABLE graph.trigger ADD CONSTRAINT trigger_kind_check
    CHECK (kind IN ('schedule'));
