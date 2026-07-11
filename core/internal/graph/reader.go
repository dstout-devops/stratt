package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// ErrNotFound is returned when a requested object does not exist.
var ErrNotFound = errors.New("graph: not found")

// GetEntity returns one Entity with its identity keys.
func (s *Store) GetEntity(ctx context.Context, id string) (types.Entity, error) {
	var e types.Entity
	var labels []byte
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, e.kind, e.labels,
		       coalesce(jsonb_object_agg(i.scheme, i.value) FILTER (WHERE i.scheme IS NOT NULL), '{}'::jsonb)
		FROM graph.entity e
		LEFT JOIN graph.entity_identity i ON i.entity_id = e.id
		WHERE e.id = $1 AND e.deleted_at IS NULL
		GROUP BY e.id`, id,
	).Scan(&e.ID, &e.Kind, &labels, &e.IdentityKeys)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, fmt.Errorf("%w: entity %s", ErrNotFound, id)
	}
	if err != nil {
		return e, fmt.Errorf("graph: get entity: %w", err)
	}
	if err := json.Unmarshal(labels, &e.Labels); err != nil {
		return e, fmt.Errorf("graph: decode labels: %w", err)
	}
	return e, nil
}

// GetFacets returns all Facets of an Entity with their Provenance — the
// "why is this value here" surface (charter §2.1, §1.8).
func (s *Store) GetFacets(ctx context.Context, entityID string) ([]types.Facet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT entity_id, namespace, value, prov_writer_kind, prov_writer_ref, coalesce(prov_source_id, ''), prov_at
		FROM graph.facet
		WHERE entity_id = $1
		ORDER BY namespace`, entityID)
	if err != nil {
		return nil, fmt.Errorf("graph: get facets: %w", err)
	}
	defer rows.Close()

	var out []types.Facet
	for rows.Next() {
		var f types.Facet
		var wk string
		if err := rows.Scan(&f.EntityID, &f.Namespace, &f.Value, &wk, &f.Provenance.WriterRef, &f.Provenance.SourceID, &f.Provenance.At); err != nil {
			return nil, fmt.Errorf("graph: scan facet: %w", err)
		}
		f.Provenance.WriterKind = types.WriterKind(wk)
		out = append(out, f)
	}
	return out, rows.Err()
}

// selectorSQL compiles a ViewSelector into a WHERE clause over graph.entity e.
// The selector is structured data (charter §2.1 — Views are declared queries,
// not an expression language); compilation is deliberately mechanical.
func selectorSQL(sel types.ViewSelector) (where string, args []any, err error) {
	conds := []string{"e.deleted_at IS NULL"}
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if len(sel.Kinds) > 0 {
		conds = append(conds, fmt.Sprintf("e.kind = ANY(%s::text[])", next(sel.Kinds)))
	}
	if len(sel.Labels) > 0 {
		doc, err := json.Marshal(sel.Labels)
		if err != nil {
			return "", nil, fmt.Errorf("graph: marshal label selector: %w", err)
		}
		conds = append(conds, fmt.Sprintf("e.labels @> %s::jsonb", next(string(doc))))
	}
	for _, p := range sel.Facets {
		if p.Namespace == "" {
			return "", nil, errors.New("graph: facet predicate requires a namespace")
		}
		doc, err := containmentDoc(p.Path, p.Equals)
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM graph.facet f WHERE f.entity_id = e.id AND f.namespace = %s AND f.value @> %s::jsonb)",
			next(p.Namespace), next(string(doc))))
	}
	return strings.Join(conds, " AND "), args, nil
}

// containmentDoc nests equals under the dotted path, producing the argument
// for a JSONB containment (@>) match: path "os.family", equals `"linux"`
// → {"os":{"family":"linux"}}.
func containmentDoc(path string, equals json.RawMessage) (json.RawMessage, error) {
	if len(equals) == 0 {
		return nil, errors.New("graph: facet predicate requires an equals value")
	}
	if !json.Valid(equals) {
		return nil, errors.New("graph: facet predicate equals is not valid JSON")
	}
	doc := equals
	if path == "" {
		return doc, nil
	}
	segs := strings.Split(path, ".")
	for i := len(segs) - 1; i >= 0; i-- {
		key, err := json.Marshal(segs[i])
		if err != nil {
			return nil, err
		}
		doc = json.RawMessage(fmt.Sprintf("{%s:%s}", key, doc))
	}
	return doc, nil
}

// ResolveSelector returns the live Entity set a selector produces, ordered by
// id for stable pagination. limit <= 0 means no limit.
func (s *Store) ResolveSelector(ctx context.Context, sel types.ViewSelector, limit int) ([]types.Entity, error) {
	where, args, err := selectorSQL(sel)
	if err != nil {
		return nil, err
	}
	q := `
		SELECT e.id, e.kind, e.labels,
		       coalesce(jsonb_object_agg(i.scheme, i.value) FILTER (WHERE i.scheme IS NOT NULL), '{}'::jsonb)
		FROM graph.entity e
		LEFT JOIN graph.entity_identity i ON i.entity_id = e.id
		WHERE ` + where + `
		GROUP BY e.id
		ORDER BY e.id`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: resolve selector: %w", err)
	}
	defer rows.Close()

	var out []types.Entity
	for rows.Next() {
		var e types.Entity
		var labels []byte
		if err := rows.Scan(&e.ID, &e.Kind, &labels, &e.IdentityKeys); err != nil {
			return nil, fmt.Errorf("graph: scan entity: %w", err)
		}
		if err := json.Unmarshal(labels, &e.Labels); err != nil {
			return nil, fmt.Errorf("graph: decode labels: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountSelector returns the size of the Entity set a selector produces —
// membership-delta and max-delta machinery build on this (§4.3).
func (s *Store) CountSelector(ctx context.Context, sel types.ViewSelector) (int64, error) {
	where, args, err := selectorSQL(sel)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM graph.entity e WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("graph: count selector: %w", err)
	}
	return n, nil
}

// ResolveView resolves a View by name to its current Entity set, returning
// the version so callers can record exactly what they targeted (§4.3).
func (s *Store) ResolveView(ctx context.Context, name string, limit int) (types.View, []types.Entity, error) {
	v, err := s.GetView(ctx, name)
	if err != nil {
		return types.View{}, nil, err
	}
	ents, err := s.ResolveSelector(ctx, v.Selector, limit)
	return v, ents, err
}
