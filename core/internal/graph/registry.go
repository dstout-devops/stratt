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
	// ADR-0060: MANY sources may own a namespace, keyed per (namespace, owner_ref).
	// Re-registering the same (namespace, owner_ref) is idempotent; a DIFFERENT
	// source registering the same namespace is now legal — no longer ErrOwnerConflict.
	// Registration still gates who MAY write (§2.5); the authoritative view is a
	// separate sources/ CaC declaration.
	// ADR-0060 declared-authority: `authoritative` marks this owner the effective
	// "truth" for the namespace. At most one per namespace (partial unique index,
	// §2.4); a conflicting second claim FAILS here rather than silently tiebreaking.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO graph.facet_owner (namespace, owner_kind, owner_ref, view_scope, authoritative)
		VALUES ($1, $2, $3, nullif($4, ''), $5)
		ON CONFLICT (namespace, owner_ref) DO UPDATE
		SET owner_kind = excluded.owner_kind, view_scope = excluded.view_scope,
		    authoritative = excluded.authoritative`,
		o.Namespace, o.OwnerKind, o.OwnerRef, o.ViewScope, o.Authoritative); err != nil {
		return fmt.Errorf("graph: register facet owner %s: %w", o.Namespace, err)
	}
	return nil
}

// GetFacetOwner returns the registered owner of a Facet namespace; ok=false
// when the namespace is unowned. The read side of the ownership eligibility
// check the compiler runs before claiming a namespace (ADR-0023).
func (s *Store) GetFacetOwner(ctx context.Context, namespace string) (types.FacetOwner, bool, error) {
	var o types.FacetOwner
	o.Namespace = namespace
	var scope *string
	err := s.pool.QueryRow(ctx,
		`SELECT owner_kind, owner_ref, view_scope FROM graph.facet_owner WHERE namespace = $1`, namespace,
	).Scan(&o.OwnerKind, &o.OwnerRef, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.FacetOwner{Namespace: namespace}, false, nil
	}
	if err != nil {
		return o, false, fmt.Errorf("graph: get facet owner: %w", err)
	}
	if scope != nil {
		o.ViewScope = *scope
	}
	return o, true, nil
}

// RegisterLabelOwner declares the single write owner of an Entity-label KEY
// (charter §2.1, ADR-0038). Idempotent for the same owner; a different owner
// fails with ErrOwnerConflict. The label equivalent of RegisterFacetOwner.
func (s *Store) RegisterLabelOwner(ctx context.Context, o types.LabelOwner) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO graph.label_owner (key, owner_kind, owner_ref, view_scope)
		VALUES ($1, $2, $3, nullif($4, ''))
		ON CONFLICT (key) DO UPDATE
		SET view_scope = excluded.view_scope
		WHERE label_owner.owner_kind = excluded.owner_kind
		  AND label_owner.owner_ref = excluded.owner_ref`,
		o.Key, o.OwnerKind, o.OwnerRef, o.ViewScope)
	if err != nil {
		return fmt.Errorf("graph: register label owner %s: %w", o.Key, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: label key %s", ErrOwnerConflict, o.Key)
	}
	return nil
}

// GetLabelOwner returns the registered owner of a label key; ok=false when the
// key is unowned.
func (s *Store) GetLabelOwner(ctx context.Context, key string) (types.LabelOwner, bool, error) {
	var o types.LabelOwner
	o.Key = key
	var scope *string
	err := s.pool.QueryRow(ctx,
		`SELECT owner_kind, owner_ref, view_scope FROM graph.label_owner WHERE key = $1`, key,
	).Scan(&o.OwnerKind, &o.OwnerRef, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.LabelOwner{Key: key}, false, nil
	}
	if err != nil {
		return o, false, fmt.Errorf("graph: get label owner: %w", err)
	}
	if scope != nil {
		o.ViewScope = *scope
	}
	return o, true, nil
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
	// A Source homes to the registering daemon's Cell (ADR-0044): its Syncer
	// runs in this Cell and its Entities inherit this home. An explicit Cell on
	// the Source wins (future CaC); otherwise the daemon's Cell.
	cell := src.Cell
	if cell == "" {
		cell = s.projCell()
	}
	// Seal-safe (ADR-0045 must-fix 3): a Connector restart on a Source currently
	// SEALED for a cross-Cell re-home must leave the row COMPLETELY untouched —
	// never rewrite its home or reset its fencing epoch mid-move (that would
	// corrupt the fenced hand-off). The DO UPDATE is gated on rehoming_to IS NULL;
	// a sealed conflict returns no row, and we return the existing (sealed) Source
	// unchanged. home_epoch is never in the SET list, so a re-register never
	// resets the fence.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO graph.source (kind, name, endpoint, credential_ref, cell)
		VALUES ($1, $2, $3, nullif($4, ''), $5)
		ON CONFLICT (name) DO UPDATE
		SET kind = excluded.kind, endpoint = excluded.endpoint,
		    credential_ref = excluded.credential_ref, cell = excluded.cell
		WHERE graph.source.rehoming_to IS NULL
		RETURNING id`,
		src.Kind, src.Name, src.Endpoint, src.CredentialRef, cell,
	).Scan(&src.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetSource(ctx, src.Name) // sealed: return it untouched
	}
	src.Cell = cell
	if err != nil {
		return src, fmt.Errorf("graph: register source: %w", err)
	}
	return src, nil
}

// GetSource returns a Source by name.
func (s *Store) GetSource(ctx context.Context, name string) (types.Source, error) {
	var src types.Source
	var cred, cell, rehoming *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, kind, name, endpoint, credential_ref, cell, rehoming_to, home_epoch
		 FROM graph.source WHERE name = $1`, name,
	).Scan(&src.ID, &src.Kind, &src.Name, &src.Endpoint, &cred, &cell, &rehoming, &src.HomeEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return src, fmt.Errorf("%w: source %s", ErrNotFound, name)
	}
	if err != nil {
		return src, fmt.Errorf("graph: get source: %w", err)
	}
	if cred != nil {
		src.CredentialRef = *cred
	}
	if cell != nil {
		src.Cell = *cell
	}
	if rehoming != nil {
		src.RehomingTo = *rehoming
	}
	return src, nil
}

// ListSources returns every registered Source (home Cell + re-home seal state),
// ordered by name — the read model behind GET /sources (ADR-0045).
func (s *Store) ListSources(ctx context.Context) ([]types.Source, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, name, endpoint, credential_ref, cell, rehoming_to, home_epoch
		 FROM graph.source ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list sources: %w", err)
	}
	defer rows.Close()
	var out []types.Source
	for rows.Next() {
		var src types.Source
		var cred, cell, rehoming *string
		if err := rows.Scan(&src.ID, &src.Kind, &src.Name, &src.Endpoint, &cred, &cell, &rehoming, &src.HomeEpoch); err != nil {
			return nil, fmt.Errorf("graph: list sources: %w", err)
		}
		if cred != nil {
			src.CredentialRef = *cred
		}
		if cell != nil {
			src.Cell = *cell
		}
		if rehoming != nil {
			src.RehomingTo = *rehoming
		}
		out = append(out, src)
	}
	return out, rows.Err()
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
