package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// SCIM identity registry (charter §8, ADR-0035). The `scim` schema is a
// born-here projection of the IdP (§1.2 — the actor model, not the estate
// graph). Two owners, both here but never overlapping:
//
//   - scim.idp — CaC config; the desired-state engine is the sole writer.
//   - scim.identity / scim.group / scim.group_member — the projection; the SCIM
//     handler is the sole writer (an IdP push, not a Principal action).

// ── scim.idp: CaC config (desired-state sole writer) ──────────────────────────

// UpsertIDP writes one declared SCIM IdP registration.
func (s *Store) UpsertIDP(ctx context.Context, d types.SCIMIdP) error {
	mappings, err := json.Marshal(d.GroupMappings)
	if err != nil {
		return fmt.Errorf("graph: marshal scim mappings: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO scim.idp (name, token_hash, mappings, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (name) DO UPDATE
		  SET token_hash = excluded.token_hash, mappings = excluded.mappings, updated_at = now()`,
		d.Name, d.TokenHash, mappings)
	if err != nil {
		return fmt.Errorf("graph: upsert scim idp: %w", err)
	}
	return nil
}

// GetIDP returns one SCIM IdP registration (token hash + mappings).
func (s *Store) GetIDP(ctx context.Context, name string) (types.SCIMIdP, error) {
	var d types.SCIMIdP
	var mappings []byte
	err := s.pool.QueryRow(ctx,
		`SELECT name, token_hash, mappings FROM scim.idp WHERE name = $1`, name).
		Scan(&d.Name, &d.TokenHash, &mappings)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("%w: scim idp %s", ErrNotFound, name)
	}
	if err != nil {
		return d, fmt.Errorf("graph: get scim idp: %w", err)
	}
	if err := json.Unmarshal(mappings, &d.GroupMappings); err != nil {
		return d, fmt.Errorf("graph: decode scim mappings: %w", err)
	}
	return d, nil
}

// ListIDPs returns every SCIM IdP registration, ordered by name.
func (s *Store) ListIDPs(ctx context.Context) ([]types.SCIMIdP, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, token_hash, mappings FROM scim.idp ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list scim idps: %w", err)
	}
	defer rows.Close()
	var out []types.SCIMIdP
	for rows.Next() {
		var d types.SCIMIdP
		var mappings []byte
		if err := rows.Scan(&d.Name, &d.TokenHash, &mappings); err != nil {
			return nil, fmt.Errorf("graph: list scim idps: %w", err)
		}
		if err := json.Unmarshal(mappings, &d.GroupMappings); err != nil {
			return nil, fmt.Errorf("graph: decode scim mappings: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteIDP removes one SCIM IdP registration.
func (s *Store) DeleteIDP(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM scim.idp WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete scim idp: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: scim idp %s", ErrNotFound, name)
	}
	return nil
}

// ── scim.identity: the projection (SCIM handler sole writer) ──────────────────

// UpsertIdentity provisions or updates one User. A re-provision of a tombstoned
// scim_id clears deleted_at (the IdP re-created the user).
func (s *Store) UpsertIdentity(ctx context.Context, i types.SCIMIdentity) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO scim.identity
		  (idp, scim_id, user_name, external_id, principal_id, active, emails, raw, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, now(), NULL)
		ON CONFLICT (idp, scim_id) DO UPDATE SET
		  user_name = excluded.user_name, external_id = excluded.external_id,
		  principal_id = excluded.principal_id, active = excluded.active,
		  emails = excluded.emails, updated_at = now(), deleted_at = NULL`,
		i.IDP, i.SCIMID, i.UserName, i.ExternalID, i.PrincipalID, i.Active,
		nullJSON(i.Emails))
	if err != nil {
		return fmt.Errorf("graph: upsert scim identity: %w", err)
	}
	return nil
}

// SetIdentityActive flips a User's active flag (SCIM PATCH active:false is the
// primary deactivation signal — the offboarding trigger).
func (s *Store) SetIdentityActive(ctx context.Context, idp, scimID string, active bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scim.identity SET active = $3, updated_at = now()
		WHERE idp = $1 AND scim_id = $2 AND deleted_at IS NULL`, idp, scimID, active)
	if err != nil {
		return fmt.Errorf("graph: set scim identity active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: scim identity %s", ErrNotFound, scimID)
	}
	return nil
}

// DeleteIdentity tombstones a User (SCIM DELETE — a hard deprovision). The row
// is kept (deleted_at set) so audit history and membership stay resolvable.
func (s *Store) DeleteIdentity(ctx context.Context, idp, scimID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scim.identity SET active = false, deleted_at = now(), updated_at = now()
		WHERE idp = $1 AND scim_id = $2 AND deleted_at IS NULL`, idp, scimID)
	if err != nil {
		return fmt.Errorf("graph: delete scim identity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: scim identity %s", ErrNotFound, scimID)
	}
	return nil
}

// GetIdentity returns one live (non-tombstoned) User.
func (s *Store) GetIdentity(ctx context.Context, idp, scimID string) (types.SCIMIdentity, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT idp, scim_id, user_name, external_id, principal_id, active, emails, created_at, updated_at
		FROM scim.identity WHERE idp = $1 AND scim_id = $2 AND deleted_at IS NULL`, idp, scimID)
	i, err := scanIdentity(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return i, fmt.Errorf("%w: scim identity %s", ErrNotFound, scimID)
	}
	return i, err
}

// ListIdentities returns live Users for an IdP, optionally filtered by an eq
// match on userName or externalId (the SCIM filters Okta/Entra use to reconcile).
func (s *Store) ListIdentities(ctx context.Context, idp, filterField, filterValue string) ([]types.SCIMIdentity, error) {
	q := `SELECT idp, scim_id, user_name, external_id, principal_id, active, emails, created_at, updated_at
	      FROM scim.identity WHERE idp = $1 AND deleted_at IS NULL`
	args := []any{idp}
	switch filterField {
	case "userName":
		q += ` AND user_name = $2`
		args = append(args, filterValue)
	case "externalId":
		q += ` AND external_id = $2`
		args = append(args, filterValue)
	}
	q += ` ORDER BY scim_id`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: list scim identities: %w", err)
	}
	defer rows.Close()
	var out []types.SCIMIdentity
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("graph: list scim identities: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// LookupActive answers the request-time deactivation gate (ADR-0035): does the
// registry know this Principal, and is it active? found=false means unknown to
// SCIM — NOT gated (never lock out service/agent or break-glass principals).
// active=true when any IdP holds a live active record for the Principal.
func (s *Store) LookupActive(ctx context.Context, principalID string) (found, active bool, err error) {
	var anyActive *bool
	err = s.pool.QueryRow(ctx, `
		SELECT bool_or(active) FROM scim.identity
		WHERE principal_id = $1 AND deleted_at IS NULL`, principalID).Scan(&anyActive)
	if err != nil {
		return false, false, fmt.Errorf("graph: scim lookup active: %w", err)
	}
	if anyActive == nil {
		return false, false, nil
	}
	return true, *anyActive, nil
}

// ── scim.group + scim.group_member: the projection ────────────────────────────

// UpsertGroup provisions or updates one Group.
func (s *Store) UpsertGroup(ctx context.Context, g types.SCIMGroup) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO scim.group (idp, scim_id, display_name, external_id, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, now(), NULL)
		ON CONFLICT (idp, scim_id) DO UPDATE SET
		  display_name = excluded.display_name, external_id = excluded.external_id,
		  updated_at = now(), deleted_at = NULL`,
		g.IDP, g.SCIMID, g.DisplayName, g.ExternalID)
	if err != nil {
		return fmt.Errorf("graph: upsert scim group: %w", err)
	}
	return nil
}

// GetGroup returns one live Group with its member scim_ids.
func (s *Store) GetGroup(ctx context.Context, idp, scimID string) (types.SCIMGroup, []string, error) {
	var g types.SCIMGroup
	err := s.pool.QueryRow(ctx, `
		SELECT idp, scim_id, display_name, external_id, created_at, updated_at
		FROM scim.group WHERE idp = $1 AND scim_id = $2 AND deleted_at IS NULL`, idp, scimID).
		Scan(&g.IDP, &g.SCIMID, &g.DisplayName, &g.ExternalID, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, nil, fmt.Errorf("%w: scim group %s", ErrNotFound, scimID)
	}
	if err != nil {
		return g, nil, fmt.Errorf("graph: get scim group: %w", err)
	}
	members, err := s.GroupMembers(ctx, idp, scimID)
	return g, members, err
}

// ListGroups returns live Groups for an IdP, optionally filtered by displayName eq.
func (s *Store) ListGroups(ctx context.Context, idp, filterField, filterValue string) ([]types.SCIMGroup, error) {
	q := `SELECT idp, scim_id, display_name, external_id, created_at, updated_at
	      FROM scim.group WHERE idp = $1 AND deleted_at IS NULL`
	args := []any{idp}
	if filterField == "displayName" {
		q += ` AND display_name = $2`
		args = append(args, filterValue)
	}
	q += ` ORDER BY scim_id`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("graph: list scim groups: %w", err)
	}
	defer rows.Close()
	var out []types.SCIMGroup
	for rows.Next() {
		var g types.SCIMGroup
		if err := rows.Scan(&g.IDP, &g.SCIMID, &g.DisplayName, &g.ExternalID, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("graph: list scim groups: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GroupMembers returns a Group's member scim_ids.
func (s *Store) GroupMembers(ctx context.Context, idp, groupSCIMID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT member_scim_id FROM scim.group_member WHERE idp = $1 AND group_scim_id = $2 ORDER BY member_scim_id`,
		idp, groupSCIMID)
	if err != nil {
		return nil, fmt.Errorf("graph: scim group members: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("graph: scim group members: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceGroupMembers sets a Group's membership to exactly the given member set
// (SCIM PUT, or a PATCH replace of the members attribute).
func (s *Store) ReplaceGroupMembers(ctx context.Context, idp, groupSCIMID string, members []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("graph: scim replace members: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`DELETE FROM scim.group_member WHERE idp = $1 AND group_scim_id = $2`, idp, groupSCIMID); err != nil {
		return fmt.Errorf("graph: scim replace members: %w", err)
	}
	for _, m := range members {
		if _, err := tx.Exec(ctx,
			`INSERT INTO scim.group_member (idp, group_scim_id, member_scim_id) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`, idp, groupSCIMID, m); err != nil {
			return fmt.Errorf("graph: scim replace members: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// AddGroupMembers adds members (SCIM PATCH add). RemoveGroupMembers removes them.
func (s *Store) AddGroupMembers(ctx context.Context, idp, groupSCIMID string, members []string) error {
	for _, m := range members {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO scim.group_member (idp, group_scim_id, member_scim_id) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`, idp, groupSCIMID, m); err != nil {
			return fmt.Errorf("graph: scim add members: %w", err)
		}
	}
	return nil
}

// RemoveGroupMembers removes members from a Group (SCIM PATCH remove).
func (s *Store) RemoveGroupMembers(ctx context.Context, idp, groupSCIMID string, members []string) error {
	for _, m := range members {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM scim.group_member WHERE idp = $1 AND group_scim_id = $2 AND member_scim_id = $3`,
			idp, groupSCIMID, m); err != nil {
			return fmt.Errorf("graph: scim remove members: %w", err)
		}
	}
	return nil
}

// DeleteGroup tombstones a Group and drops its membership edges.
func (s *Store) DeleteGroup(ctx context.Context, idp, scimID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("graph: delete scim group: %w", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE scim.group SET deleted_at = now(), updated_at = now()
		WHERE idp = $1 AND scim_id = $2 AND deleted_at IS NULL`, idp, scimID)
	if err != nil {
		return fmt.Errorf("graph: delete scim group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: scim group %s", ErrNotFound, scimID)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM scim.group_member WHERE idp = $1 AND group_scim_id = $2`, idp, scimID); err != nil {
		return fmt.Errorf("graph: delete scim group: %w", err)
	}
	return tx.Commit(ctx)
}

// ── reconcile read: projected memberships → authz tuples ──────────────────────

// ProjectedMemberships resolves every CaC group→team mapping against the live
// projection: each active member of a mapped group yields one (PrincipalID,
// Team) edge. The reconcile loop turns these into team:<t>#member tuples and
// unions them with the CaC tuple set before the authoritative OpenFGA sync
// (ADR-0035). Deactivated / tombstoned identities and identities without a
// principal_id are excluded — deactivation drops the grant.
func (s *Store) ProjectedMemberships(ctx context.Context) ([]types.SCIMMembership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT i.principal_id, m.team
		FROM scim.idp d
		CROSS JOIN LATERAL jsonb_to_recordset(d.mappings) AS m("group" text, team text)
		JOIN scim.group g
		  ON g.idp = d.name AND g.display_name = m."group" AND g.deleted_at IS NULL
		JOIN scim.group_member gm
		  ON gm.idp = d.name AND gm.group_scim_id = g.scim_id
		JOIN scim.identity i
		  ON i.idp = d.name AND i.scim_id = gm.member_scim_id
		 AND i.deleted_at IS NULL AND i.active = true AND i.principal_id <> ''
		ORDER BY m.team, i.principal_id`)
	if err != nil {
		return nil, fmt.Errorf("graph: scim projected memberships: %w", err)
	}
	defer rows.Close()
	var out []types.SCIMMembership
	for rows.Next() {
		var mm types.SCIMMembership
		if err := rows.Scan(&mm.PrincipalID, &mm.Team); err != nil {
			return nil, fmt.Errorf("graph: scim projected memberships: %w", err)
		}
		out = append(out, mm)
	}
	return out, rows.Err()
}

// MappedTeams returns the set of teams any IdP mapping targets — the reconcile
// one-owner guard checks these against CaC-declared team membership (§2.1).
func (s *Store) MappedTeams(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT m.team
		FROM scim.idp d
		CROSS JOIN LATERAL jsonb_to_recordset(d.mappings) AS m("group" text, team text)`)
	if err != nil {
		return nil, fmt.Errorf("graph: scim mapped teams: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("graph: scim mapped teams: %w", err)
		}
		out[t] = true
	}
	return out, rows.Err()
}

func scanIdentity(row pgx.Row) (types.SCIMIdentity, error) {
	var i types.SCIMIdentity
	var emails []byte
	err := row.Scan(&i.IDP, &i.SCIMID, &i.UserName, &i.ExternalID, &i.PrincipalID, &i.Active, &emails, &i.CreatedAt, &i.UpdatedAt)
	if len(emails) > 0 {
		i.Emails = emails
	}
	return i, err
}

// rawOrNil keeps the raw SCIM body column populated when we have emails but no
// separate raw payload threaded through; callers may pass a full raw body via a
// dedicated setter later. For now the projection stores emails; raw stays NULL.
func rawOrNil(_ json.RawMessage) json.RawMessage { return nil }
