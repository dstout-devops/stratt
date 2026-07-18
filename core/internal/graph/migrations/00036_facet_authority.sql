-- ADR-0060 declared-authority (the second half of the model): retaining ALL
-- per-source signal is only half the design — the operator also declares which
-- source is the effective "truth" for a namespace, so a scalar read resolves to
-- ONE value instead of failing safe. Authority is a per-namespace flag on the
-- ownership registry (never a precedence/priority field, §2.4): AT MOST ONE owner
-- per namespace may be authoritative, enforced by a partial unique index. Undeclared
-- contention still surfaces a Finding (the resolver omits when no authority is
-- declared). sources/ CaC (ADR-0056) later just SETS this flag from Git — the
-- mechanism is stable, so that lands additively with no rework.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE graph.facet_owner ADD COLUMN authoritative boolean NOT NULL DEFAULT false;

-- §2.4: exactly-one-or-none authoritative owner per namespace — never an implicit
-- tiebreak between two claimed truths. A second authority claim FAILS the write.
CREATE UNIQUE INDEX facet_owner_one_authority
    ON graph.facet_owner (namespace) WHERE authoritative;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS graph.facet_owner_one_authority;
ALTER TABLE graph.facet_owner DROP COLUMN authoritative;
-- +goose StatementEnd
