package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dstout-devops/stratt/types"
)

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLState 23505).
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Evidence manifests (ADR-0029) point at sealed audit bundles in the object
// store. The object store holds the immutable artifact; these rows are the
// graph's rebuildable projection of it (§1.2).

// RecordEvidence writes one Evidence manifest. The unique index on finding_id
// makes it write-once: a duplicate seal returns ErrConflict, never a second row.
func (s *Store) RecordEvidence(ctx context.Context, e types.Evidence) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.evidence
			(finding_id, baseline, target, object_key, sha256, size_bytes, retain_until)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.FindingID, e.Baseline, e.Target, e.ObjectKey, e.SHA256, e.SizeBytes, e.RetainUntil)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: evidence for finding %s already sealed", ErrConflict, e.FindingID)
		}
		return fmt.Errorf("graph: record evidence: %w", err)
	}
	return nil
}

const evidenceColumns = `id, finding_id, baseline, target, object_key, sha256, size_bytes, sealed_at, retain_until`

func scanEvidence(row pgx.Row) (types.Evidence, error) {
	var e types.Evidence
	if err := row.Scan(&e.ID, &e.FindingID, &e.Baseline, &e.Target, &e.ObjectKey,
		&e.SHA256, &e.SizeBytes, &e.SealedAt, &e.RetainUntil); err != nil {
		return e, err
	}
	return e, nil
}

// GetEvidenceByFinding returns the Evidence manifest for a Finding, or
// ErrNotFound when the Finding has no sealed bundle.
func (s *Store) GetEvidenceByFinding(ctx context.Context, findingID string) (types.Evidence, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+evidenceColumns+` FROM graph.evidence WHERE finding_id = $1`, findingID)
	e, err := scanEvidence(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Evidence{}, fmt.Errorf("%w: evidence for finding %s", ErrNotFound, findingID)
	}
	if err != nil {
		return types.Evidence{}, fmt.Errorf("graph: get evidence: %w", err)
	}
	return e, nil
}

// GetEvidence returns one Evidence manifest by its own id.
func (s *Store) GetEvidence(ctx context.Context, id string) (types.Evidence, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+evidenceColumns+` FROM graph.evidence WHERE id = $1`, id)
	e, err := scanEvidence(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Evidence{}, fmt.Errorf("%w: evidence %s", ErrNotFound, id)
	}
	if err != nil {
		return types.Evidence{}, fmt.Errorf("graph: get evidence: %w", err)
	}
	return e, nil
}

// ListUnsealedFindings returns open Findings that have no Evidence manifest yet
// — the retry-safe seal work-list (§4.3, ADR-0029). Keyed by the manifest's
// existence, not the one-shot pending→open transition, so a failed+retried seal
// re-picks the missed Findings instead of losing them. Scoped to a baseline
// (empty = all).
func (s *Store) ListUnsealedFindings(ctx context.Context, baseline string) ([]types.Finding, error) {
	q := `SELECT ` + findingColumns + ` FROM graph.finding f
		WHERE f.status = 'open'
		  AND ($1 = '' OR f.baseline = $1)
		  AND NOT EXISTS (SELECT 1 FROM graph.evidence e WHERE e.finding_id = f.id)
		ORDER BY f.opened_at`
	rows, err := s.pool.Query(ctx, q, baseline)
	if err != nil {
		return nil, fmt.Errorf("graph: list unsealed findings: %w", err)
	}
	defer rows.Close()
	var out []types.Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("graph: list unsealed findings: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
