-- +goose Up
-- Versioned Intent documents, EXPAND half (ADR-0119 D1/D7).
--
-- An Intent becomes identified by (name, version) so test/stage/prod can run three
-- configurations of one Intent simultaneously — the property graph.blueprint has had since
-- 00013 ("versioned: (name, version) is the identity so an upgrade rolls through rings
-- alongside the old version") and graph.intent has not.
--
-- THIS IS THE EXPAND RELEASE AND IT IS ADDITIVE ONLY. ADR-0078 runs migrations in a
-- pre-upgrade hook Job while the PREVIOUS release's replicas are still serving, and those
-- replicas write `ON CONFLICT (name)`. Dropping the (name) primary key here would fail every
-- Intent upsert for the whole roll window with "no unique or exclusion constraint matching
-- the ON CONFLICT specification". So the old PK STAYS, a unique index on (name, version) is
-- added beside it, and new code writes `ON CONFLICT (name, version)` — which the added index
-- satisfies. Old and new replicas both work.
--
-- CONSEQUENCE, stated because it is easy to mistake this migration for the whole feature:
-- while the (name) PK survives, two versions of one name CANNOT coexist. Rings do not light
-- up in this release. The contract migration (a later release, after every replica is new)
-- drops the (name) PK and promotes (name, version); until then the desired-state loader
-- rejects a second version of a name with a real message rather than letting a raw
-- duplicate-key error surface (ADR-0119 D7).
--
-- `version int NOT NULL DEFAULT 1` is safe under the expand/contract lint precisely because
-- it has a default: an old replica inserting without the column still produces a valid row.
ALTER TABLE graph.intent
    ADD COLUMN IF NOT EXISTS version int NOT NULL DEFAULT 1;

-- The index new code's ON CONFLICT targets. Also the identity the contract release promotes
-- to the primary key, so it is created here rather than there — index builds are the slow
-- part, and doing it in expand keeps the contract step to a fast catalogue change.
CREATE UNIQUE INDEX IF NOT EXISTS intent_name_version_key
    ON graph.intent (name, version);

-- +goose Down
-- Reversible: the column and its index are additive, and dropping them restores the pre-expand
-- shape exactly. Written explicitly per ADR-0078 follow-up MIG-1 — a migration without a Down
-- is a one-way door, and this one does not need to be.
DROP INDEX IF EXISTS graph.intent_name_version_key;
ALTER TABLE graph.intent
    DROP COLUMN IF EXISTS version;
