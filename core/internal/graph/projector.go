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
	// cell is the writing Cell id stamped as prov_cell (ADR-0044), inherited
	// from the Store. LocalCell for the single-Cell default.
	cell string
}

// NormalizerProjector returns the write surface for Connector Normalizers.
func (s *Store) NormalizerProjector() *Projector {
	return &Projector{pool: s.pool, path: WritePathNormalizer, cell: s.projCell()}
}

// RunProjector returns the write surface for Run-provenance fact writes.
func (s *Store) RunProjector() *Projector {
	return &Projector{pool: s.pool, path: WritePathRunProvenance, cell: s.projCell()}
}

// projCell returns the Store's Cell id, defaulting to LocalCell (a Store built
// outside Connect, e.g. in a test, has an empty cell).
func (s *Store) projCell() string {
	if s.cell == "" {
		return types.LocalCell
	}
	return s.cell
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
	// Transaction-local declarations checked by the §1.2 triggers: the write
	// PATH, and (ADR-0045) the projecting daemon's CELL — the home gate rejects a
	// Normalizer projection for a Source homed on a different Cell.
	if _, err := tx.Exec(ctx, `
		SELECT set_config('stratt.write_path', $1, true),
		       set_config('stratt.cell', $2, true)`, string(p.path), p.cell); err != nil {
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
		id, err := upsertEntityTx(ctx, tx, prov, p.cell, e)
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

func upsertEntityTx(ctx context.Context, tx pgx.Tx, prov types.Provenance, cell string, e EntityUpsert) (string, error) {
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
		// home_cell (residency, ADR-0044) is stamped ONCE here, at creation, =
		// the creating daemon's Cell. It is deliberately NOT written on the
		// correlate-UPDATE branch below: an Entity keeps its home even when a
		// different Cell's daemon observes it, and that retained divergence is
		// exactly what the placement-mismatch sweep reports (§2.4). The only
		// other mutation is the fenced re-home (slice 7).
		if err := tx.QueryRow(ctx, `
			INSERT INTO graph.entity (kind, labels, prov_writer_kind, prov_writer_ref, prov_source_id, prov_cell, home_cell, prov_at)
			VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $6, $7)
			RETURNING id`,
			e.Kind, labels, string(prov.WriterKind), prov.WriterRef, prov.SourceID, cell, prov.At,
		).Scan(&id); err != nil {
			return "", fmt.Errorf("graph: insert entity: %w", err)
		}
	case 1:
		id = matched[0]
		// Per-key MERGE, not whole-blob replace (ADR-0041): this writer
		// contributes only its own label keys; other Sources' keys on a
		// correlated Entity are preserved (no §2.4 cross-source clobber, and a
		// no-label writer no longer wipes). Ownership of the changed keys is
		// enforced by the enforce_label_owner trigger.
		if _, err := tx.Exec(ctx, `
			UPDATE graph.entity
			SET kind = $2, labels = graph.entity.labels || $3::jsonb,
			    prov_writer_kind = $4, prov_writer_ref = $5,
			    prov_source_id = nullif($6, ''), prov_cell = $8, prov_at = $7,
			    deleted_at = NULL
			WHERE id = $1`,
			id, e.Kind, labels, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At, cell,
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

	// Record this Source's presence (ADR-0042): liveness is a UNION over
	// Sources, so each Syncer observation asserts a per-(Entity, Source) row.
	// Run writes record none — a run-only Entity stays outside the presence
	// system and is never tombstoned.
	if prov.WriterKind == types.WriterSyncer && prov.SourceID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO graph.entity_presence (entity_id, source_id, first_seen, last_seen)
			VALUES ($1, $2::uuid, now(), now())
			ON CONFLICT (entity_id, source_id) DO UPDATE SET last_seen = now()`,
			id, prov.SourceID,
		); err != nil {
			return "", fmt.Errorf("graph: record presence: %w", err)
		}
	}

	for ns, val := range e.Facets {
		if err := upsertFacetTx(ctx, tx, prov, cell, id, ns, val); err != nil {
			return "", err
		}
	}
	return id, nil
}

func upsertFacetTx(ctx context.Context, tx pgx.Tx, prov types.Provenance, cell, entityID, namespace string, value json.RawMessage) error {
	// Pinned Facet schemas validate at the write path itself (§1.5,
	// ADR-0015) — every writer (Normalizer and Run provenance alike) passes
	// through here, so enforcement is structural, not a review norm.
	// Namespaces without a demanded schema pass uncovered (§1.1).
	if _, err := contract.ValidateFacet(namespace, value); err != nil {
		return fmt.Errorf("graph: facet %s on %s: %w", namespace, entityID, err)
	}
	// The Facet grain includes the SOURCE (ADR-0060): each source retains its own
	// row, so two sources projecting one namespace never overwrite each other. A
	// write with no Source (a Run/Actuator write-back) uses the empty-string key —
	// NOT NULL (a valid PK dimension), bounded (never per-Run writer_ref), and
	// skipped by the home-gate uuid cast (`sid <> ''`). A genuine per-Actuator
	// source is the ADR-0060 M2 follow-up.
	source := prov.SourceID
	if _, err := tx.Exec(ctx, `
		INSERT INTO graph.facet (entity_id, namespace, value, prov_writer_kind, prov_writer_ref, prov_source_id, prov_cell, prov_at)
		VALUES ($1, $2, $3, $4, $5, $6, $8, $7)
		ON CONFLICT (entity_id, namespace, prov_source_id) DO UPDATE
		SET value = excluded.value,
		    prov_writer_kind = excluded.prov_writer_kind,
		    prov_writer_ref = excluded.prov_writer_ref,
		    prov_cell = excluded.prov_cell,
		    prov_at = excluded.prov_at`,
		entityID, namespace, value, string(prov.WriterKind), prov.WriterRef, source, prov.At, cell,
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
	if err := upsertFacetTx(ctx, tx, prov, p.cell, entityID, namespace, value); err != nil {
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
	var relID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO graph.relation (type, from_id, to_id, prov_writer_kind, prov_writer_ref, prov_source_id, prov_cell, prov_at)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $8, $7)
		ON CONFLICT (type, from_id, to_id) DO UPDATE
		SET prov_writer_kind = excluded.prov_writer_kind,
		    prov_writer_ref = excluded.prov_writer_ref,
		    prov_source_id = excluded.prov_source_id,
		    prov_cell = excluded.prov_cell,
		    prov_at = excluded.prov_at
		RETURNING id`,
		relType, fromID, toID, string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At, p.cell,
	).Scan(&relID); err != nil {
		return fmt.Errorf("graph: upsert relation: %w", err)
	}
	// Relation liveness (ADR-0082): an OBSERVED edge (from a Source) records this
	// Source's presence, so liveness is a UNION over Sources. A run-provenance edge
	// (no Source) writes no presence and keeps the run/cascade lifecycle.
	if prov.SourceID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO graph.relation_presence (relation_id, source_id, last_seen)
			VALUES ($1, $2, now())
			ON CONFLICT (relation_id, source_id) DO UPDATE SET last_seen = now()`,
			relID, prov.SourceID,
		); err != nil {
			return fmt.Errorf("graph: record relation presence: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// RetractRelation deletes one edge — the Syncer delta-retraction half of relation
// GC (ADR-0059 decision 7a): a connector that stops observing an edge (the Observe
// stream's GoneRelations) retracts it, so a placement edge never dangles after its
// Source stops asserting it. Idempotent (a missing edge is a no-op). The §1.2
// relation_write_path trigger is satisfied by the projector's write-path GUC; a
// DELETE skips the writer-kind agreement check, so a Syncer may retract the edge it
// (as a Source) wrote. The write PATH (normalizer/run) comes from this projector.
func (p *Projector) RetractRelation(ctx context.Context, relType, fromID, toID string) error {
	tx, err := p.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM graph.relation WHERE type = $1 AND from_id = $2 AND to_id = $3`,
		relType, fromID, toID,
	); err != nil {
		return fmt.Errorf("graph: retract relation: %w", err)
	}
	return tx.Commit(ctx)
}

// RetractRunRelationsFrom deletes the caller's OWN (Run-provenance) edges of relType from
// fromID whose target is NOT keepTo — the MOVE half of a singular placement relation
// (ADR-0059): a host is placed-in ONE subnet, so re-projecting its placement retracts the
// stale edge rather than leaving it in two. Scoped to prov_writer_kind='run' so a build
// NEVER clobbers a Syncer's OBSERVED edge (cross-source respect, §1.2). Idempotent; the
// §1.2 relation_write_path trigger is satisfied by the projector's run-provenance GUC.
//
// §2.4 SINGLE-OWNING-BUILD RELIANCE (guardian-flagged): the run-wide scope would be
// last-writer-wins if TWO builds placed one from-Entity. That is prevented upstream by the
// provisioning EXCLUSIVE CLAIM (provision.Plan / PlanSingletons: one Intent per
// (intentKind, name) / instance name is a compile error, §2.4) — so exactly one Intent
// owns a unit, hence one buildWorkflow, hence one owning build projects its placement.
// The proper STRUCTURAL guard (a Relation-ownership registry, the edge analogue of the
// Facet ownership registry §2.1, catching an ad-hoc second workflow that hand-places the
// same host) is a named follow-up — until it lands, the exclusive-claim is the guard and a
// manual double-build of one host is an operator declaration error, not a silent tiebreak.
func (p *Projector) RetractRunRelationsFrom(ctx context.Context, relType, fromID, keepTo string) error {
	tx, err := p.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM graph.relation WHERE type = $1 AND from_id = $2 AND to_id <> $3 AND prov_writer_kind = 'run'`,
		relType, fromID, keepTo,
	); err != nil {
		return fmt.Errorf("graph: retract run relations from: %w", err)
	}
	return tx.Commit(ctx)
}

// RetractSourceRelationPresenceExcept is a Source's full-sync presence replace over
// relation liveness (ADR-0082): it retracts THIS Source's presence for its edges of
// relType EXCEPT the (keepFrom[i], keepTo[i]) pairs re-emitted this cycle, then
// deletes any observed edge of relType whose LAST presence is now gone. A co-asserted
// edge survives one Source dropping it (union liveness, the ADR-0042 rule for edges);
// an edge asserted only by this Source is collected. An empty keep-set retracts all
// of this Source's presence for relType (the type-fully-gone case). Returns the
// number of EDGES deleted. Supersedes the ADR-0081 single-source direct delete.
func (p *Projector) RetractSourceRelationPresenceExcept(ctx context.Context, sourceID, relType string, keepFrom, keepTo []string) (int64, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// 1. Retract this Source's presence for edges of relType it no longer emits.
	if _, err := tx.Exec(ctx, `
		DELETE FROM graph.relation_presence rp
		USING graph.relation r
		WHERE rp.relation_id = r.id AND rp.source_id = $1 AND r.type = $2
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest($3::uuid[], $4::uuid[]) AS k(f, t)
		    WHERE k.f = r.from_id AND k.t = r.to_id)`,
		sourceID, relType, keepFrom, keepTo,
	); err != nil {
		return 0, fmt.Errorf("graph: retract relation presence: %w", err)
	}
	// 2. Delete observed edges of relType whose LAST presence row is now gone (no
	// Source still asserts them). Run-provenance edges (no Source) are untouched.
	tag, err := tx.Exec(ctx, `
		DELETE FROM graph.relation r
		WHERE r.type = $1 AND r.prov_writer_kind = 'syncer'
		  AND NOT EXISTS (SELECT 1 FROM graph.relation_presence rp WHERE rp.relation_id = r.id)`,
		relType,
	)
	if err != nil {
		return 0, fmt.Errorf("graph: delete dead relations: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("graph: commit: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RelationTypesBySource returns the distinct relation types a Source currently
// ASSERTS — so a full-sync replace also sweeps a type the Source stopped emitting
// entirely. It reads relation_presence (the true per-Source asserting set, ADR-0082),
// NOT graph.relation.prov_source_id (a LAST-WRITER field): a multi-source edge
// co-asserted by this Source but last-written by another would otherwise be invisible
// here, so this Source's presence for it would never be retracted (guardian MF-2).
func (s *Store) RelationTypesBySource(ctx context.Context, sourceID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT r.type
		FROM graph.relation_presence rp
		JOIN graph.relation r ON r.id = rp.relation_id
		WHERE rp.source_id = $1`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("graph: relation types by source: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// retractRelationsFor deletes every relation touching any of ids — the endpoint-
// tombstone cascade (ADR-0059 decision 7b). Entities are SOFT-deleted (deleted_at),
// so the relation FK's ON DELETE CASCADE never fires; a decommissioned endpoint must
// not leave a dangling placement (or any) edge, so the tombstone path sweeps them
// explicitly. Runs INSIDE the caller's tombstone tx, so the §1.2 write-path GUC is
// already set; a relation DELETE skips the writer-kind check, so this retracts BOTH
// syncer- and run-provenance edges of the gone endpoint (a build-Run-written
// placement edge is never re-observed by any Syncer, so only this cascade collects it).
func retractRelationsFor(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM graph.relation WHERE from_id = ANY($1::uuid[]) OR to_id = ANY($1::uuid[])`,
		ids,
	); err != nil {
		return fmt.Errorf("graph: cascade-retract relations: %w", err)
	}
	return nil
}

// TombstoneAbsent retracts the calling Source's presence for every Entity it
// carries an identity for under scheme but whose value is not in seen — the
// disappearance half of a full resync. An Entity is tombstoned only when its
// LAST Source's presence is retracted (ADR-0042): liveness is a union over
// Sources, so a host co-managed by another Source stays live. Returns the
// number of Entities actually tombstoned. The projection stays rebuildable;
// tombstones keep Run history resolvable.
func (p *Projector) TombstoneAbsent(ctx context.Context, prov types.Provenance, scheme string, seen []string) (int64, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Stmt A: retract this Source's presence for the vanished Entities. A
	// data-modifying CTE cannot be used here — its DELETE is invisible to a
	// NOT EXISTS in the same statement under Postgres snapshot rules — so the
	// tombstone is a second statement in the same transaction (Stmt B).
	rows, err := tx.Query(ctx, `
		DELETE FROM graph.entity_presence p
		USING graph.entity_identity i
		WHERE p.entity_id = i.entity_id
		  AND p.source_id = $1::uuid
		  AND i.scheme = $2
		  AND NOT (i.value = ANY($3::text[]))
		RETURNING p.entity_id`,
		prov.SourceID, scheme, seen,
	)
	if err != nil {
		return 0, fmt.Errorf("graph: retract presence: %w", err)
	}
	retracted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("graph: collect retracted: %w", err)
	}
	if len(retracted) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return 0, nil
	}

	// Stmt B: tombstone only the retracted Entities whose LAST presence row is
	// now gone, restamping the retracting Syncer's provenance (which keeps the
	// enforce_write_path prov-check satisfied on the normalizer path).
	rows2, err := tx.Query(ctx, `
		UPDATE graph.entity e
		SET deleted_at = now(),
		    prov_writer_kind = $1, prov_writer_ref = $2, prov_source_id = nullif($3, ''), prov_at = $4
		WHERE e.id = ANY($5::uuid[])
		  AND e.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM graph.entity_presence p2 WHERE p2.entity_id = e.id)
		RETURNING e.id`,
		string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At, retracted,
	)
	if err != nil {
		return 0, fmt.Errorf("graph: tombstone absent: %w", err)
	}
	tombstoned, err := pgx.CollectRows(rows2, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("graph: collect tombstoned: %w", err)
	}
	// Cascade: retract every edge touching a now-tombstoned endpoint (decision 7b).
	if err := retractRelationsFor(ctx, tx, tombstoned); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(tombstoned)), nil
}

// TombstoneByIdentity retracts the calling Source's presence for the single
// Entity carrying the given identity key — the disappearance half of delta
// ingestion — and tombstones it only if that was its last Source's presence
// (ADR-0042). Returns true iff the Entity was tombstoned.
func (p *Projector) TombstoneByIdentity(ctx context.Context, prov types.Provenance, scheme, value string) (bool, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Stmt A: retract this Source's presence for the one (scheme, value).
	rows, err := tx.Query(ctx, `
		DELETE FROM graph.entity_presence p
		USING graph.entity_identity i
		WHERE p.entity_id = i.entity_id
		  AND p.source_id = $1::uuid
		  AND i.scheme = $2 AND i.value = $3
		RETURNING p.entity_id`,
		prov.SourceID, scheme, value,
	)
	if err != nil {
		return false, fmt.Errorf("graph: retract presence: %w", err)
	}
	retracted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("graph: collect retracted: %w", err)
	}
	if len(retracted) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}

	// Stmt B: tombstone the Entity iff its last presence row is now gone.
	rows2, err := tx.Query(ctx, `
		UPDATE graph.entity e
		SET deleted_at = now(),
		    prov_writer_kind = $1, prov_writer_ref = $2, prov_source_id = nullif($3, ''), prov_at = $4
		WHERE e.id = ANY($5::uuid[])
		  AND e.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM graph.entity_presence p2 WHERE p2.entity_id = e.id)
		RETURNING e.id`,
		string(prov.WriterKind), prov.WriterRef, prov.SourceID, prov.At, retracted,
	)
	if err != nil {
		return false, fmt.Errorf("graph: tombstone by identity: %w", err)
	}
	tombstoned, err := pgx.CollectRows(rows2, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("graph: collect tombstoned: %w", err)
	}
	// Cascade: retract every edge touching a now-tombstoned endpoint (decision 7b).
	if err := retractRelationsFor(ctx, tx, tombstoned); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return len(tombstoned) > 0, nil
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
