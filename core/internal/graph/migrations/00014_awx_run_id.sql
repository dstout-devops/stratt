-- +goose Up
-- AWX /api/v2 façade stable-integer job ids (charter §5.6, ADR-0026). AWX
-- tooling addresses jobs by an INTEGER id (terraform parses with strconv.Atoi;
-- awxkit interpolates into URLs), but a Stratt Run is a uuid. The façade
-- synthesizes the AWX id as a hash of the uuid, computable IDENTICALLY in Go
-- (crypto/md5 → first 4 bytes big-endian → & 0x7fffffff) and here in SQL.
--
-- This stores NO new datum and adds NO mapping table (§1.5 — no second source
-- of truth): the id is a pure function of graph.run.id, and the functional
-- index below just makes the reverse lookup (awx id → Run) an indexed query
-- instead of a scan. Drop the index and every id is still recomputable.
--
-- Int31 collisions become likely at very large run counts; the façade's reverse
-- lookup resolves to the most-recent Run sharing the id, which is correct for
-- transient job polling (a client polls a job id only right after its launch).

-- +goose StatementBegin
CREATE FUNCTION graph.awx_run_id(u uuid) RETURNS bigint
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
  SELECT (('x' || substr(md5(u::text), 1, 8))::bit(32)::bigint) & 2147483647
$$;
-- +goose StatementEnd

CREATE INDEX run_awx_id_idx ON graph.run (graph.awx_run_id(id));

-- +goose Down
DROP INDEX IF EXISTS graph.run_awx_id_idx;
DROP FUNCTION IF EXISTS graph.awx_run_id(uuid);
