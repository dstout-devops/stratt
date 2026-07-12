package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/types"
)

// WritePath is one of the two legal graph write paths (charter §1.2).
type WritePath string

const (
	// WritePathNormalizer marks writes projected from a Source by a
	// Connector's Normalizer.
	WritePathNormalizer WritePath = "normalizer"
	// WritePathRunProvenance marks facts a Run writes back directly
	// (§4.3 flap damping).
	WritePathRunProvenance WritePath = "run-provenance"
)

// Projector is the only write surface for the graph projection. Every
// transaction it opens declares its write path as a transaction-local
// Postgres setting; the triggers installed by the migrations reject writes
// arriving without it, so no other code path can mutate
// Entities/Facets/Relations (charter §1.2 — enforced in the data layer).
type Projector struct {
	pool *pgxpool.Pool
	path WritePath
}

// NormalizerProjector returns the write surface for Connector Normalizers.
func (s *Store) NormalizerProjector() *Projector {
	return &Projector{pool: s.pool, path: WritePathNormalizer}
}

// RunProjector returns the write surface for Run-provenance fact writes.
func (s *Store) RunProjector() *Projector {
	return &Projector{pool: s.pool, path: WritePathRunProvenance}
}

// EntityUpsert is one observed Entity to project. Correlation happens on
// IdentityKeys: if any (scheme, value) already belongs to an Entity, the
// observation updates that Entity; otherwise a new Entity is created.
type EntityUpsert struct {
	Kind         string
	IdentityKeys map[string]string
	Labels       map[string]string
	// Facets to project alongside, keyed by namespace. Each namespace must
	// be registered in the facet-ownership registry or the write fails.
	Facets map[string]json.RawMessage
}

// ErrIdentityConflict is returned when one observation's identity keys match
// two different existing Entities. Phase 0 refuses to merge; the conflict is
// surfaced, never silently resolved.
var ErrIdentityConflict = errors.New("graph: identity keys match multiple entities")

func (p *Projector) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph: begin: %w", err)
	}
	// Transaction-local declaration checked by the §1.2 triggers.
	if _, err := tx.Exec(ctx, `SELECT set_config('stratt.write_path', $1, true)`, string(p.path)); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("graph: declare write path: %w", err)
	}
	return tx, nil
}

// UpsertEntities projects a batch of observed Entities and their Facets in
// one transaction, stamping every row with prov. Returns the Entity ids in
// input order.
func (p *Projector) UpsertEntities(ctx context.Context, prov types.Provenance, batch []EntityUpsert) ([]string, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	ids := make([]string, len(batch))
	for i, e := range batch {
		id, err := upsertEntityTx(ctx, tx, prov, e)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("graph: commit: %w", err)
	}
	return ids, nil
}

func upsertEntityTx(ctx context.Context, tx pgx.Tx, prov types.Provenance, e EntityUpsert) (string, error) {
	if len(e.IdentityKeys) == 0 {
		return "", errors.New("graph: entity upsert requires at least one identity key")
	}
	labels, err := json.Marshal(orEmpty(e.Labels))
	if err != nil {
		return "", fmt.Errorf("graph: marshal labels: %w", err)
	}

	// Correlate on identity keys.
	schemes := make([]string, 0, len(e.IdentityKeys))
	values := make([]string, 0, len(e.IdentityKeys))
	for s, v := range e.IdentityKeys {
		schemes = append(schemes, s)
		values = append(values, v)
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT entity_id
		FROM graph.entity_identity
		JOIN unnest($1::text[], $2::text[]) AS k(scheme, value) USING (scheme, value)`,
		schemes, values)
	if err != nil {
		return "", fmt.Errorf("graph: correlate identities: %w", err)
	}
	matched, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("graph: collect correlation: %w", err)
	}

	var id string
	switch len(matched) {
	case 0:
		if err := tx.QueryRow(ctx, `
			INSERT INTO graph.entity (kind, labels, prov_writer_kind, prov_writer_ref, prov_source_id, prov_at)
			VALUES ($1, $2, $3, $4, nullif($5, ''), $6)
			RETURNING id`,
			e.Kind, labels, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At,
		).Scan(&id); err != nil {
			return "", fmt.Errorf("graph: insert entity: %w", err)
		}
	case 1:
		id = matched[0]
		if _, err := tx.Exec(ctx, `
			UPDATE graph.entity
			SET kind = $2, labels = $3,
			    prov_writer_kind = $4, prov_writer_ref = $5,
			    prov_source_id = nullif($6, ''), prov_at = $7,
			    deleted_at = NULL
			WHERE id = $1`,
			id, e.Kind, labels, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At,
		); err != nil {
			return "", fmt.Errorf("graph: update entity: %w", err)
		}
	default:
		return "", fmt.Errorf("%w: keys %v match %d entities", ErrIdentityConflict, e.IdentityKeys, len(matched))
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO graph.entity_identity (entity_id, scheme, value)
		SELECT $1, k.scheme, k.value
		FROM unnest($2::text[], $3::text[]) AS k(scheme, value)
		ON CONFLICT (entity_id, scheme) DO UPDATE SET value = excluded.value`,
		id, schemes, values,
	); err != nil {
		return "", fmt.Errorf("graph: upsert identities: %w", err)
	}

	for ns, val := range e.Facets {
		if err := upsertFacetTx(ctx, tx, prov, id, ns, val); err != nil {
			return "", err
		}
	}
	return id, nil
}

func upsertFacetTx(ctx context.Context, tx pgx.Tx, prov types.Provenance, entityID, namespace string, value json.RawMessage) error {
	// Pinned Facet schemas validate at the write path itself (§1.5,
	// ADR-0015) — every writer (Normalizer and Run provenance alike) passes
	// through here, so enforcement is structural, not a review norm.
	// Namespaces without a demanded schema pass uncovered (§1.1).
	if _, err := contract.ValidateFacet(namespace, value); err != nil {
		return fmt.Errorf("graph: facet %s on %s: %w", namespace, entityID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO graph.facet (entity_id, namespace, value, prov_writer_kind, prov_writer_ref, prov_source_id, prov_at)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7)
		ON CONFLICT (entity_id, namespace) DO UPDATE
		SET value = excluded.value,
		    prov_writer_kind = excluded.prov_writer_kind,
		    prov_writer_ref = excluded.prov_writer_ref,
		    prov_source_id = excluded.prov_source_id,
		    prov_at = excluded.prov_at`,
		entityID, namespace, value, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At,
	); err != nil {
		return fmt.Errorf("graph: upsert facet %s on %s: %w", namespace, entityID, err)
	}
	return nil
}

// UpsertFacet projects one Facet value onto an existing Entity.
func (p *Projector) UpsertFacet(ctx context.Context, prov types.Provenance, entityID, namespace string, value json.RawMessage) error {
	tx, err := p.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := upsertFacetTx(ctx, tx, prov, entityID, namespace, value); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpsertRelation projects one typed directed edge.
func (p *Projector) UpsertRelation(ctx context.Context, prov types.Provenance, relType, fromID, toID string) error {
	tx, err := p.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO graph.relation (type, from_id, to_id, prov_writer_kind, prov_writer_ref, prov_source_id, prov_at)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7)
		ON CONFLICT (type, from_id, to_id) DO UPDATE
		SET prov_writer_kind = excluded.prov_writer_kind,
		    prov_writer_ref = excluded.prov_writer_ref,
		    prov_source_id = excluded.prov_source_id,
		    prov_at = excluded.prov_at`,
		relType, fromID, toID, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At,
	); err != nil {
		return fmt.Errorf("graph: upsert relation: %w", err)
	}
	return tx.Commit(ctx)
}

// TombstoneAbsent marks as deleted every live Entity that carries an identity
// under scheme but whose identity value is not in seen — the disappearance
// half of a full resync. The projection stays rebuildable; tombstones keep
// Run history resolvable.
func (p *Projector) TombstoneAbsent(ctx context.Context, prov types.Provenance, scheme string, seen []string) (int64, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
		UPDATE graph.entity e
		SET deleted_at = now(),
		    prov_writer_kind = $3, prov_writer_ref = $4, prov_source_id = nullif($5, ''), prov_at = $6
		FROM graph.entity_identity i
		WHERE i.entity_id = e.id
		  AND i.scheme = $1
		  AND e.deleted_at IS NULL
		  AND NOT (i.value = ANY($2::text[]))`,
		scheme, seen, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At,
	)
	if err != nil {
		return 0, fmt.Errorf("graph: tombstone absent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// TombstoneByIdentity marks as deleted the single live Entity carrying the
// given identity key — the disappearance half of delta ingestion.
func (p *Projector) TombstoneByIdentity(ctx context.Context, prov types.Provenance, scheme, value string) (bool, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
		UPDATE graph.entity e
		SET deleted_at = now(),
		    prov_writer_kind = $3, prov_writer_ref = $4, prov_source_id = nullif($5, ''), prov_at = $6
		FROM graph.entity_identity i
		WHERE i.entity_id = e.id
		  AND i.scheme = $1 AND i.value = $2
		  AND e.deleted_at IS NULL`,
		scheme, value, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At,
	)
	if err != nil {
		return false, fmt.Errorf("graph: tombstone by identity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
