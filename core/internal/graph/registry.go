package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// ErrOwnerConflict is returned when a Facet namespace already has a different
// registered owner. Two writers to one namespace is a registration error
// (charter §2.1) — surfaced, never resolved by precedence.
var ErrOwnerConflict = errors.New("graph: facet namespace already has a different owner")

// RegisterFacetOwner declares the single write owner of a Facet namespace.
// Registering the same owner again is idempotent; registering a different
// owner fails with ErrOwnerConflict.
func (s *Store) RegisterFacetOwner(ctx context.Context, o types.FacetOwner) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO graph.facet_owner (namespace, owner_kind, owner_ref, view_scope)
		VALUES ($1, $2, $3, nullif($4, ''))
		ON CONFLICT (namespace) DO UPDATE
		SET view_scope = excluded.view_scope
		WHERE facet_owner.owner_kind = excluded.owner_kind
		  AND facet_owner.owner_ref = excluded.owner_ref`,
		o.Namespace, o.OwnerKind, o.OwnerRef, o.ViewScope)
	if err != nil {
		return fmt.Errorf("graph: register facet owner %s: %w", o.Namespace, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: namespace %s", ErrOwnerConflict, o.Namespace)
	}
	return nil
}

// ── Views (§2.1) ─────────────────────────────────────────────────────────────

// DeclareView creates or updates a View's selector. Every change bumps the
// version and lands in view_history (trigger-enforced). In Phase 1 this is
// driven by the Git sync controller; direct calls are the Phase-0 path.
func (s *Store) DeclareView(ctx context.Context, name string, sel types.ViewSelector) (types.View, error) {
	selDoc, err := json.Marshal(sel)
	if err != nil {
		return types.View{}, fmt.Errorf("graph: marshal selector: %w", err)
	}
	var v types.View
	var raw []byte
	err = s.pool.QueryRow(ctx, `
		INSERT INTO graph.view (name, selector)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET selector = excluded.selector
		WHERE graph.view.selector IS DISTINCT FROM excluded.selector
		RETURNING name, version, selector`,
		name, selDoc,
	).Scan(&v.Name, &v.Version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		// Unchanged selector: a no-op declare, not a new version (the Git
		// sync controller re-declares every reconcile in Phase 1).
		return s.GetView(ctx, name)
	}
	if err != nil {
		return types.View{}, fmt.Errorf("graph: declare view: %w", err)
	}
	if err := json.Unmarshal(raw, &v.Selector); err != nil {
		return types.View{}, fmt.Errorf("graph: decode selector: %w", err)
	}
	return v, nil
}

// GetView returns a View by name.
func (s *Store) GetView(ctx context.Context, name string) (types.View, error) {
	var v types.View
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT name, version, selector FROM graph.view WHERE name = $1`, name,
	).Scan(&v.Name, &v.Version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, fmt.Errorf("%w: view %s", ErrNotFound, name)
	}
	if err != nil {
		return v, fmt.Errorf("graph: get view: %w", err)
	}
	if err := json.Unmarshal(raw, &v.Selector); err != nil {
		return v, fmt.Errorf("graph: decode selector: %w", err)
	}
	return v, nil
}

// ── Sources (§2.2) ───────────────────────────────────────────────────────────

// RegisterSource registers an external system of record. The credentialRef is
// a pointer only — secret material never persists in the platform (§2.5).
func (s *Store) RegisterSource(ctx context.Context, src types.Source) (types.Source, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO graph.source (kind, name, endpoint, credential_ref)
		VALUES ($1, $2, $3, nullif($4, ''))
		ON CONFLICT (name) DO UPDATE
		SET kind = excluded.kind, endpoint = excluded.endpoint, credential_ref = excluded.credential_ref
		RETURNING id`,
		src.Kind, src.Name, src.Endpoint, src.CredentialRef,
	).Scan(&src.ID)
	if err != nil {
		return src, fmt.Errorf("graph: register source: %w", err)
	}
	return src, nil
}

// GetSource returns a Source by name.
func (s *Store) GetSource(ctx context.Context, name string) (types.Source, error) {
	var src types.Source
	var cred *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, kind, name, endpoint, credential_ref FROM graph.source WHERE name = $1`, name,
	).Scan(&src.ID, &src.Kind, &src.Name, &src.Endpoint, &cred)
	if errors.Is(err, pgx.ErrNoRows) {
		return src, fmt.Errorf("%w: source %s", ErrNotFound, name)
	}
	if err != nil {
		return src, fmt.Errorf("graph: get source: %w", err)
	}
	if cred != nil {
		src.CredentialRef = *cred
	}
	return src, nil
}

// SyncCursor returns the stored delta cursor for a Source ("" if none).
func (s *Store) SyncCursor(ctx context.Context, sourceID string) (string, error) {
	var cursor *string
	err := s.pool.QueryRow(ctx,
		`SELECT cursor FROM graph.source_sync WHERE source_id = $1`, sourceID,
	).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) || cursor == nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("graph: sync cursor: %w", err)
	}
	return *cursor, nil
}

// SetSyncCursor records the delta cursor after a successful sync pass.
func (s *Store) SetSyncCursor(ctx context.Context, sourceID, cursor string, full bool) error {
	col := "last_delta_at"
	if full {
		col = "last_full_sync_at"
	}
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO graph.source_sync (source_id, cursor, %[1]s)
		VALUES ($1, $2, now())
		ON CONFLICT (source_id) DO UPDATE SET cursor = excluded.cursor, %[1]s = now()`, col),
		sourceID, cursor)
	if err != nil {
		return fmt.Errorf("graph: set sync cursor: %w", err)
	}
	return nil
}
