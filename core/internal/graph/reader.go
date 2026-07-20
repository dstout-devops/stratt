package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/core/internal/template"
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

// EntityIDByIdentity resolves a LIVE Entity's id by one of its identity keys —
// the relation-targeting primitive (ADR-0047 §1: resolve-don't-vivify). found is
// false when no live Entity carries that (scheme, value); the caller drops the
// edge and records a rejection, NEVER creating a placeholder Entity (which would
// covertly write an ungranted identity key).
func (s *Store) EntityIDByIdentity(ctx context.Context, scheme, value string) (string, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT i.entity_id
		FROM graph.entity_identity i
		JOIN graph.entity e ON e.id = i.entity_id AND e.deleted_at IS NULL
		WHERE i.scheme = $1 AND i.value = $2
		LIMIT 1`, scheme, value).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("graph: resolve identity %s=%s: %w", scheme, value, err)
	}
	return id, true, nil
}

// EntityWriterKind returns the WriterKind that created an Entity ("syncer" or
// "run") — the per-verb provenance distinction (ADR-0047 §2: Observe write-back
// is Syncer-provenance, Apply/Invoke write-back is Run-provenance). For
// descent/tests.
func (s *Store) EntityWriterKind(ctx context.Context, id string) (string, error) {
	var wk string
	err := s.pool.QueryRow(ctx, `SELECT prov_writer_kind FROM graph.entity WHERE id = $1`, id).Scan(&wk)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: entity %s", ErrNotFound, id)
	}
	if err != nil {
		return "", fmt.Errorf("graph: entity writer kind: %w", err)
	}
	return wk, nil
}

// RelationTargets returns the ids of the Entities that fromID points to via a
// relation of relType — the descent/edge surface (§1.8).
func (s *Store) RelationTargets(ctx context.Context, fromID, relType string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_id FROM graph.relation WHERE from_id = $1 AND type = $2`, fromID, relType)
	if err != nil {
		return nil, fmt.Errorf("graph: relation targets: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RelationSources returns the from_ids of every relation of relType pointing AT toID —
// the incoming-edge twin of RelationTargets. Used by the adopt cutover guard to find the
// foreign-side executions (e.g. the AWX schedules that `schedules` a template) still
// targeting an object being adopted (ADR-0086 §4).
func (s *Store) RelationSources(ctx context.Context, toID, relType string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT from_id FROM graph.relation WHERE to_id = $1 AND type = $2`, toID, relType)
	if err != nil {
		return nil, fmt.Errorf("graph: relation sources: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
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

// GetObservedBy returns the Sources that currently observe an Entity and when
// each last saw it — the per-Source presence set backing cross-source liveness
// (charter §1.2, ADR-0042). The true presence set, replacing the last-writer
// prov_source_id guess.
func (s *Store) GetObservedBy(ctx context.Context, entityID string) ([]types.SourceObservation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT src.id, src.kind, src.name, p.first_seen, p.last_seen
		FROM graph.entity_presence p
		JOIN graph.source src ON src.id = p.source_id
		WHERE p.entity_id = $1
		ORDER BY src.name`, entityID)
	if err != nil {
		return nil, fmt.Errorf("graph: get observed-by: %w", err)
	}
	defer rows.Close()

	var out []types.SourceObservation
	for rows.Next() {
		var o types.SourceObservation
		if err := rows.Scan(&o.SourceID, &o.Kind, &o.Name, &o.FirstSeen, &o.LastSeen); err != nil {
			return nil, fmt.Errorf("graph: scan observed-by: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// FacetValuesByEntities bulk-reads one Facet namespace across a set of Entities
// — the dispatch-routing read path (ADR-0032): a single query for mgmt.site
// over a resolved View, avoiding an N+1 GetFacets fan-out. Entities without the
// Facet are simply absent from the returned map.
func (s *Store) FacetValuesByEntities(ctx context.Context, namespace string, entityIDs []string) (map[string]json.RawMessage, error) {
	if len(entityIDs) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	// ADR-0060 M5 + declared-authority: a scalar read resolves ONE effective value
	// per Entity from now-possibly-many per-source rows — never a last-row/last-writer
	// pick. Each row is flagged is_authority when its writer is the namespace's
	// DECLARED authoritative owner (facet_owner.authoritative — at most one, §2.4):
	// a syncer stamps prov_writer_ref = its owner_ref, so the join is exact. Run
	// (empty-source) write-backs never match an authoritative syncer-owner, so a
	// build's as-applied value defers to the declared IPAM/SoR truth by construction.
	rows, err := s.pool.Query(ctx, `
		SELECT f.entity_id, f.value,
		       (o.owner_ref IS NOT NULL) AS is_authority
		FROM graph.facet f
		LEFT JOIN graph.facet_owner o
		  ON o.namespace = f.namespace AND o.owner_ref = f.prov_writer_ref AND o.authoritative
		WHERE f.namespace = $1 AND f.entity_id = ANY($2::uuid[])`, namespace, entityIDs)
	if err != nil {
		return nil, fmt.Errorf("graph: facet values by entities: %w", err)
	}
	defer rows.Close()
	type candidate struct {
		val         json.RawMessage
		isAuthority bool
	}
	perEntity := make(map[string][]candidate, len(entityIDs))
	for rows.Next() {
		var id string
		var val json.RawMessage
		var isAuthority bool
		if err := rows.Scan(&id, &val, &isAuthority); err != nil {
			return nil, fmt.Errorf("graph: scan facet value: %w", err)
		}
		perEntity[id] = append(perEntity[id], candidate{val, isAuthority})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Resolve: a single source yields its value. Many sources resolve to the ONE
	// declared authority if present; with none declared the read is OMITTED
	// (fail-safe — e.g. mgmt.site routing falls to the default) so nothing is
	// silently picked, and the ownership-contention Finding surfaces it (§2.4/§1.8).
	out := make(map[string]json.RawMessage, len(perEntity))
	for id, cands := range perEntity {
		if len(cands) == 1 {
			out[id] = cands[0].val
			continue
		}
		var auth []json.RawMessage
		for _, c := range cands {
			if c.isAuthority {
				auth = append(auth, c.val)
			}
		}
		if len(auth) == 1 {
			out[id] = auth[0]
		}
	}
	return out, nil
}

// HomeCellsByEntities bulk-reads the home Cell of a set of Entities (ADR-0044)
// — the cross-Cell analogue of FacetValuesByEntities/mgmt.site, feeding the Run
// Cell-union now and slice 3's federation router's write-forwarding later. Every
// live Entity has a home_cell (NOT NULL), so every id in the input maps.
func (s *Store) HomeCellsByEntities(ctx context.Context, entityIDs []string) (map[string]string, error) {
	if len(entityIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, home_cell FROM graph.entity WHERE id = ANY($1::uuid[])`, entityIDs)
	if err != nil {
		return nil, fmt.Errorf("graph: home cells by entities: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string, len(entityIDs))
	for rows.Next() {
		var id, cell string
		if err := rows.Scan(&id, &cell); err != nil {
			return nil, fmt.Errorf("graph: scan home cell: %w", err)
		}
		out[id] = cell
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
	// Relation predicates (ADR-0059 decision 6): the Entity has an outgoing edge of
	// Type to a live target of TargetKind carrying TargetLabels. An EXISTS join over
	// graph.relation — topology-aware selection, additive (AND) with the other clauses.
	for _, rp := range sel.Relations {
		if rp.Type == "" {
			return "", nil, errors.New("graph: relation predicate requires a type")
		}
		rc := []string{
			fmt.Sprintf("r.from_id = e.id AND r.type = %s", next(rp.Type)),
			"te.deleted_at IS NULL",
		}
		if rp.TargetKind != "" {
			rc = append(rc, fmt.Sprintf("te.kind = %s", next(rp.TargetKind)))
		}
		if len(rp.TargetLabels) > 0 {
			doc, err := json.Marshal(rp.TargetLabels)
			if err != nil {
				return "", nil, fmt.Errorf("graph: marshal relation target labels: %w", err)
			}
			rc = append(rc, fmt.Sprintf("te.labels @> %s::jsonb", next(string(doc))))
		}
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM graph.relation r JOIN graph.entity te ON te.id = r.to_id WHERE %s)",
			strings.Join(rc, " AND ")))
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
// id for stable pagination. limit <= 0 means no limit. A selector carrying
// {{.param.x}} placeholders (a parametrized View, ADR-0024) is resolved
// against params first; a non-parametrized selector ignores params.
func (s *Store) ResolveSelector(ctx context.Context, sel types.ViewSelector, params map[string]any, limit int) ([]types.Entity, error) {
	sel, err := bindSelector(sel, params)
	if err != nil {
		return nil, err
	}
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
// the version so callers can record exactly what they targeted (§4.3). params
// binds a parametrized View's placeholders (ADR-0024); nil for a plain View.
func (s *Store) ResolveView(ctx context.Context, name string, params map[string]any, limit int) (types.View, []types.Entity, error) {
	v, err := s.GetView(ctx, name)
	if err != nil {
		return types.View{}, nil, err
	}
	ents, err := s.ResolveSelector(ctx, v.Selector, params, limit)
	return v, ents, err
}

// bindSelector resolves {{.param.x}} placeholders in a copy of the selector
// (Labels values and Facet Equals) against params — the parametrized-View
// binding (ADR-0024). selectorSQL then sees fully-resolved structured data.
// A selector with no placeholders returns unchanged.
func bindSelector(sel types.ViewSelector, params map[string]any) (types.ViewSelector, error) {
	hasTemplate := false
	for _, v := range sel.Labels {
		if strings.Contains(v, "{{") {
			hasTemplate = true
		}
	}
	for _, f := range sel.Facets {
		if strings.Contains(string(f.Equals), "{{") {
			hasTemplate = true
		}
	}
	if !hasTemplate {
		return sel, nil
	}
	ns := template.Namespaces{"param": params}
	// Relations carry through unbound (topology predicates take no {{.param}} today).
	out := types.ViewSelector{Kinds: sel.Kinds, Relations: sel.Relations}
	if sel.Labels != nil {
		out.Labels = make(map[string]string, len(sel.Labels))
		for k, v := range sel.Labels {
			r, err := template.Substitute(v, ns)
			if err != nil {
				return sel, fmt.Errorf("graph: view label %q: %w", k, err)
			}
			out.Labels[k] = fmt.Sprint(r)
		}
	}
	for _, f := range sel.Facets {
		nf := f
		if strings.Contains(string(f.Equals), "{{") {
			bound, err := bindRaw(f.Equals, ns)
			if err != nil {
				return sel, fmt.Errorf("graph: view facet %q: %w", f.Namespace, err)
			}
			nf.Equals = bound
		}
		out.Facets = append(out.Facets, nf)
	}
	return out, nil
}

// bindRaw substitutes templates inside a JSON value (type-preserving).
func bindRaw(raw json.RawMessage, ns template.Namespaces) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, nil
	}
	out, err := template.Substitute(v, ns)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
