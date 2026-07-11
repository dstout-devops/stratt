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

// Declaration paths for Views (§1.2: desired state lives in Git).
const (
	// DeclaredByAPI marks a directly-declared View (PUT /views/{name}).
	DeclaredByAPI = "api"
	// DeclaredByCaC marks a View owned by the declared desired state (the
	// Git sync controller / stratt apply).
	DeclaredByCaC = "cac"
)

// ErrDeclaredByCaC is returned when the api path tries to modify a View owned
// by the declared desired state — Git-declared Views are Git-only (§2.1).
var ErrDeclaredByCaC = errors.New("graph: view is declared by desired state (cac); edit it in the declarations repo")

// DeclareView creates or updates a View's selector via the api path. It is
// refused with ErrDeclaredByCaC for Views owned by the desired state.
func (s *Store) DeclareView(ctx context.Context, name string, sel types.ViewSelector) (types.View, error) {
	return s.DeclareViewAs(ctx, name, sel, DeclaredByAPI)
}

// DeclareViewAs creates or updates a View's selector for the given
// declaration path. Selector changes bump the version and land in
// view_history (trigger-enforced); an unchanged declare is a version-stable
// no-op. CaC may adopt an api-declared View (ownership transfers, version
// unchanged); the api path may never touch a cac View — enforced by the
// upsert guard, not read-then-write.
func (s *Store) DeclareViewAs(ctx context.Context, name string, sel types.ViewSelector, declaredBy string) (types.View, error) {
	selDoc, err := json.Marshal(sel)
	if err != nil {
		return types.View{}, fmt.Errorf("graph: marshal selector: %w", err)
	}
	var v types.View
	var raw []byte
	err = s.pool.QueryRow(ctx, `
		INSERT INTO graph.view (name, selector, declared_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
		SET selector = excluded.selector, declared_by = excluded.declared_by
		WHERE (excluded.declared_by = 'cac' OR graph.view.declared_by = 'api')
		  AND (graph.view.selector IS DISTINCT FROM excluded.selector
		       OR graph.view.declared_by IS DISTINCT FROM excluded.declared_by)
		RETURNING name, version, selector, declared_by`,
		name, selDoc, declaredBy,
	).Scan(&v.Name, &v.Version, &raw, &v.DeclaredBy)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either a no-op declare (unchanged selector and owner) or an api
		// write refused by the cac guard — fetch to tell them apart.
		existing, getErr := s.GetView(ctx, name)
		if getErr != nil {
			return types.View{}, getErr
		}
		if declaredBy == DeclaredByAPI && existing.DeclaredBy == DeclaredByCaC {
			return types.View{}, fmt.Errorf("%w: view %s", ErrDeclaredByCaC, name)
		}
		return existing, nil
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
		`SELECT name, version, selector, declared_by FROM graph.view WHERE name = $1`, name,
	).Scan(&v.Name, &v.Version, &raw, &v.DeclaredBy)
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

// ListViews returns every View, ordered by name.
func (s *Store) ListViews(ctx context.Context) ([]types.View, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, version, selector, declared_by FROM graph.view ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list views: %w", err)
	}
	defer rows.Close()
	var out []types.View
	for rows.Next() {
		var v types.View
		var raw []byte
		if err := rows.Scan(&v.Name, &v.Version, &raw, &v.DeclaredBy); err != nil {
			return nil, fmt.Errorf("graph: list views: %w", err)
		}
		if err := json.Unmarshal(raw, &v.Selector); err != nil {
			return nil, fmt.Errorf("graph: decode selector: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListViewsDeclaredBy returns every View owned by the given declaration path,
// ordered by name — the prune set for desired-state reconciliation.
func (s *Store) ListViewsDeclaredBy(ctx context.Context, declaredBy string) ([]types.View, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, version, selector, declared_by FROM graph.view WHERE declared_by = $1 ORDER BY name`,
		declaredBy)
	if err != nil {
		return nil, fmt.Errorf("graph: list views: %w", err)
	}
	defer rows.Close()
	var out []types.View
	for rows.Next() {
		var v types.View
		var raw []byte
		if err := rows.Scan(&v.Name, &v.Version, &raw, &v.DeclaredBy); err != nil {
			return nil, fmt.Errorf("graph: list views: %w", err)
		}
		if err := json.Unmarshal(raw, &v.Selector); err != nil {
			return nil, fmt.Errorf("graph: decode selector: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteView removes a View owned by the given declaration path, along with
// its version history (Runs keep view_ref and view_version by value, and the
// declarations repo remains the rebuildable source, §1.2). A View owned by a
// different path is not touched — ErrNotFound either way.
//
// Consequence: view_history is NOT an audit source across a delete boundary —
// a re-declared name restarts at version 1, so the selector a historical Run
// pinned is recoverable only from Git history after delete/recreate. Never
// join Run.view_version against view_history across that boundary.
func (s *Store) DeleteView(ctx context.Context, name, declaredBy string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("graph: delete view: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx,
		`DELETE FROM graph.view WHERE name = $1 AND declared_by = $2`, name, declaredBy)
	if err != nil {
		return fmt.Errorf("graph: delete view: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: view %s (declared_by %s)", ErrNotFound, name, declaredBy)
	}
	// History must go with the row: a later re-declare restarts at version 1
	// and would collide with the retained (name, version) history keys.
	if _, err := tx.Exec(ctx,
		`DELETE FROM graph.view_history WHERE name = $1`, name); err != nil {
		return fmt.Errorf("graph: delete view history: %w", err)
	}
	return tx.Commit(ctx)
}

// ── CredentialRefs (§2.5, ADR-0009) ─────────────────────────────────────────

// DeclareCredentialRefAs creates or updates a CredentialRef pointer for the
// given declaration path — the same cac-over-api ownership asymmetry as
// Views (§1.2): cac may adopt, api may never touch a cac ref.
func (s *Store) DeclareCredentialRefAs(ctx context.Context, ref types.CredentialRef, declaredBy string) (types.CredentialRef, error) {
	injection, err := json.Marshal(ref.Injection)
	if err != nil {
		return types.CredentialRef{}, fmt.Errorf("graph: marshal injection: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO graph.credential_ref (name, owner_team, backend, locator, injection, declared_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE
		SET owner_team = excluded.owner_team, backend = excluded.backend,
		    locator = excluded.locator, injection = excluded.injection,
		    declared_by = excluded.declared_by
		WHERE (excluded.declared_by = 'cac' OR graph.credential_ref.declared_by = 'api')`,
		ref.Name, ref.OwnerTeam, ref.Backend, ref.Locator, injection, declaredBy)
	if err != nil {
		return types.CredentialRef{}, fmt.Errorf("graph: declare credential ref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return types.CredentialRef{}, fmt.Errorf("%w: credential ref %s", ErrDeclaredByCaC, ref.Name)
	}
	return s.GetCredentialRef(ctx, ref.Name)
}

// GetCredentialRef returns one pointer. There is no method anywhere that
// returns material — no such code path exists (ADR-0009).
func (s *Store) GetCredentialRef(ctx context.Context, name string) (types.CredentialRef, error) {
	var ref types.CredentialRef
	var injection []byte
	err := s.pool.QueryRow(ctx, `
		SELECT name, owner_team, backend, locator, injection, declared_by
		FROM graph.credential_ref WHERE name = $1`, name,
	).Scan(&ref.Name, &ref.OwnerTeam, &ref.Backend, &ref.Locator, &injection, &ref.DeclaredBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ref, fmt.Errorf("%w: credential ref %s", ErrNotFound, name)
	}
	if err != nil {
		return ref, fmt.Errorf("graph: get credential ref: %w", err)
	}
	if err := json.Unmarshal(injection, &ref.Injection); err != nil {
		return ref, fmt.Errorf("graph: decode injection: %w", err)
	}
	return ref, nil
}

// ListCredentialRefsDeclaredBy returns pointers owned by a declaration path.
func (s *Store) ListCredentialRefsDeclaredBy(ctx context.Context, declaredBy string) ([]types.CredentialRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, owner_team, backend, locator, injection, declared_by
		FROM graph.credential_ref WHERE declared_by = $1 ORDER BY name`, declaredBy)
	if err != nil {
		return nil, fmt.Errorf("graph: list credential refs: %w", err)
	}
	defer rows.Close()
	var out []types.CredentialRef
	for rows.Next() {
		var ref types.CredentialRef
		var injection []byte
		if err := rows.Scan(&ref.Name, &ref.OwnerTeam, &ref.Backend, &ref.Locator, &injection, &ref.DeclaredBy); err != nil {
			return nil, fmt.Errorf("graph: list credential refs: %w", err)
		}
		if err := json.Unmarshal(injection, &ref.Injection); err != nil {
			return nil, fmt.Errorf("graph: decode injection: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// DeleteCredentialRef removes a pointer owned by the given declaration path.
func (s *Store) DeleteCredentialRef(ctx context.Context, name, declaredBy string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM graph.credential_ref WHERE name = $1 AND declared_by = $2`, name, declaredBy)
	if err != nil {
		return fmt.Errorf("graph: delete credential ref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: credential ref %s (declared_by %s)", ErrNotFound, name, declaredBy)
	}
	return nil
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
