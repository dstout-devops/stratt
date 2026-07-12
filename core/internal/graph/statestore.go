package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// OpenTofu state rows (ADR-0016). The data column is ciphertext — encryption
// and decryption live in the statebackend package; this store never sees
// plaintext state.

// GetOpenTofuState returns the ciphertext and current lock document ("" /
// nil when absent).
func (s *Store) GetOpenTofuState(ctx context.Context, workspace string) (data []byte, lock []byte, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT data, lock FROM graph.opentofu_state WHERE workspace = $1`, workspace,
	).Scan(&data, &lock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("graph: get opentofu state: %w", err)
	}
	return data, lock, nil
}

// PutOpenTofuState upserts the ciphertext.
func (s *Store) PutOpenTofuState(ctx context.Context, workspace string, data []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.opentofu_state (workspace, data)
		VALUES ($1, $2)
		ON CONFLICT (workspace) DO UPDATE SET data = excluded.data, updated_at = now()`,
		workspace, data)
	if err != nil {
		return fmt.Errorf("graph: put opentofu state: %w", err)
	}
	return nil
}

// LockOpenTofuState stores the lock document if the workspace is unlocked;
// returns the holder's document (held=true) when already locked.
func (s *Store) LockOpenTofuState(ctx context.Context, workspace string, lock []byte) (held bool, holder []byte, err error) {
	// Upsert the row so a lock can precede the first state write.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO graph.opentofu_state (workspace, data, lock)
		VALUES ($1, ''::bytea, $2)
		ON CONFLICT (workspace) DO UPDATE SET lock = excluded.lock, updated_at = now()
		WHERE graph.opentofu_state.lock IS NULL`,
		workspace, lock)
	if err != nil {
		return false, nil, fmt.Errorf("graph: lock opentofu state: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return false, nil, nil
	}
	_, holder, err = s.GetOpenTofuState(ctx, workspace)
	return true, holder, err
}

// UnlockOpenTofuState clears the lock unconditionally (tofu sends the lock
// doc it holds; force-unlock sends without one — both clear).
func (s *Store) UnlockOpenTofuState(ctx context.Context, workspace string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE graph.opentofu_state SET lock = NULL, updated_at = now() WHERE workspace = $1`,
		workspace)
	if err != nil {
		return fmt.Errorf("graph: unlock opentofu state: %w", err)
	}
	return nil
}
