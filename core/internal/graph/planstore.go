package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/core/internal/planstore"
)

// Saved-plan artifact rows (ADR-0047 §8). The data column is AES-256-GCM
// ciphertext — encryption/decryption and content-addressing live in the planstore
// package; this store never sees plaintext. graph.Store satisfies
// planstore.ArtifactDB.

// PutPlanArtifact stores ciphertext under the plaintext digest, WRITE-ONCE: a
// second write of the same digest is a no-op (content-addressed idempotency — a
// fixed digest is never re-pointed at different bytes).
func (s *Store) PutPlanArtifact(ctx context.Context, sha256Hex string, ciphertext []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.plan_artifact (sha256, data)
		VALUES ($1, $2)
		ON CONFLICT (sha256) DO NOTHING`,
		sha256Hex, ciphertext)
	if err != nil {
		return fmt.Errorf("graph: put plan artifact: %w", err)
	}
	return nil
}

// GetPlanArtifact returns the stored ciphertext for a digest, or
// planstore.ErrNotFound when absent — so a pinned Apply whose plan is missing
// fails closed, never falling back to an unpinned apply (ADR-0047 §8).
func (s *Store) GetPlanArtifact(ctx context.Context, sha256Hex string) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM graph.plan_artifact WHERE sha256 = $1`, sha256Hex,
	).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, planstore.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("graph: get plan artifact: %w", err)
	}
	return data, nil
}
