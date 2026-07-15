-- +goose Up
-- Link a notification delivery to its descendable Run (§1.8, ADR-0040): the
-- notifier now delivers via RunAction, so each delivery is a first-class Run.
-- Nullable — pre-0040 deliveries have none.
ALTER TABLE graph.notify_delivery ADD COLUMN run_id uuid;

-- +goose Down
ALTER TABLE graph.notify_delivery DROP COLUMN run_id;
