package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Sites (ADR-0032) are CaC-declared: the desired-state engine is the sole
// writer, mirroring View/Trigger/Emitter/notify_sink. Discrete columns rather
// than a spec blob — a Site has a fixed, small shape and the mode CHECK is
// enforced in the schema. The built-in "local" locus is never stored (the
// name <> 'local' CHECK refuses it).

// UpsertSite writes one declared Site.
func (s *Store) UpsertSite(ctx context.Context, st types.Site) error {
	declaredBy := st.DeclaredBy
	if declaredBy == "" {
		declaredBy = "cac"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.site (name, mode, namespace, description, declared_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET
			mode = excluded.mode, namespace = excluded.namespace,
			description = excluded.description, declared_by = excluded.declared_by`,
		st.Name, st.Mode, st.Namespace, st.Description, declaredBy)
	if err != nil {
		return fmt.Errorf("graph: upsert site: %w", err)
	}
	return nil
}

// GetSite returns one Site declaration.
func (s *Store) GetSite(ctx context.Context, name string) (types.Site, error) {
	var st types.Site
	var namespace, description *string
	err := s.pool.QueryRow(ctx, `
		SELECT name, mode, namespace, description, declared_by
		FROM graph.site WHERE name = $1`, name,
	).Scan(&st.Name, &st.Mode, &namespace, &description, &st.DeclaredBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Site{}, fmt.Errorf("%w: site %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Site{}, fmt.Errorf("graph: get site: %w", err)
	}
	if namespace != nil {
		st.Namespace = *namespace
	}
	if description != nil {
		st.Description = *description
	}
	return st, nil
}

// ListSites returns every Site declaration, ordered by name.
func (s *Store) ListSites(ctx context.Context) ([]types.Site, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, mode, namespace, description, declared_by
		FROM graph.site ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list sites: %w", err)
	}
	defer rows.Close()
	var out []types.Site
	for rows.Next() {
		var st types.Site
		var namespace, description *string
		if err := rows.Scan(&st.Name, &st.Mode, &namespace, &description, &st.DeclaredBy); err != nil {
			return nil, fmt.Errorf("graph: list sites: %w", err)
		}
		if namespace != nil {
			st.Namespace = *namespace
		}
		if description != nil {
			st.Description = *description
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// DeleteSite removes one Site declaration.
func (s *Store) DeleteSite(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.site WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete site: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: site %s", ErrNotFound, name)
	}
	return nil
}
