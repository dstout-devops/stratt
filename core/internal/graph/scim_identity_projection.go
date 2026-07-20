package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/types"
)

// scimIdentityProjector is the WriterRef + facet-owner ref for the SCIM→graph
// identity projection (ADR-0079 slice 3). One name, so the §2.1 facet-ownership
// registry and the write provenance agree.
const scimIdentityProjector = "scim-identity-projector"

// EnsureIdentitySubjectOwner registers this projector as the single write-owner of
// the identity.subject Facet namespace AND the identity.name label key (§2.1 /
// ADR-0041 / ADR-0079 slice-3 gate). Idempotent; call once at boot before the
// reconcile projects. A second transport (a pull syncer) may not claim either
// without displacing this owner — two writers to one subject's identity is a
// registration error, not a merge.
func (s *Store) EnsureIdentitySubjectOwner(ctx context.Context) error {
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "identity.subject",
		OwnerKind: string(types.WriterSyncer),
		OwnerRef:  scimIdentityProjector,
	}); err != nil {
		return err
	}
	return s.RegisterLabelOwner(ctx, types.LabelOwner{
		Key:       "identity.name",
		OwnerKind: string(types.WriterSyncer),
		OwnerRef:  scimIdentityProjector,
	})
}

// ProjectSCIMEntities projects the SCIM registry (users + groups) into the graph
// as `user`/`group` Entities carrying identity.subject, with member-of Relations
// (ADR-0079 slice 3). This is what makes identity a first-class estate citizen:
// Views/Baselines/Findings now range over people the way they range over hosts.
//
// Charter discipline: the graph is a REBUILDABLE read-model of the SCIM registry,
// which is itself a projection of the IdP system of record (§1.2). This Normalizer
// and Run provenance are the only writers of these attributes (INV-1); the status
// is projected from the SoR and never authored here (INV-2). Provenance stamps the
// per-IdP Source so every projected identity is attributable.
//
// Best-effort per IdP: a failing IdP is logged by the caller and does not abort
// the others; the projection is idempotent (re-runs converge).
func (s *Store) ProjectSCIMEntities(ctx context.Context) error {
	idps, err := s.ListIDPs(ctx)
	if err != nil {
		return fmt.Errorf("scim-identity-projection: list idps: %w", err)
	}
	proj := s.NormalizerProjector()
	for _, idp := range idps {
		src, err := s.RegisterSource(ctx, types.Source{Kind: "scim", Name: "scim:" + idp.Name})
		if err != nil {
			return fmt.Errorf("scim-identity-projection: register source %q: %w", idp.Name, err)
		}
		prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: scimIdentityProjector, SourceID: src.ID}

		// Users → `user` Entities.
		users, err := s.ListIdentities(ctx, idp.Name, "", "")
		if err != nil {
			return fmt.Errorf("scim-identity-projection: list identities %q: %w", idp.Name, err)
		}
		userBatch := make([]EntityUpsert, 0, len(users))
		for _, u := range users {
			status := "active"
			if !u.Active {
				status = "disabled"
			}
			subj := map[string]any{"scheme": "user", "name": u.UserName, "authority": idp.Name, "status": status}
			if u.ExternalID != "" {
				subj["externalId"] = u.ExternalID
			}
			// authenticates-as (ADR-0079 slice 4): record the Principal this identity
			// authenticates as, as a CORRELATION attribute — bridges the audit/run/cost
			// plane (Principal-keyed) to the estate identity without a principal graph
			// node (no plane merge). Never read by authz (INV-3).
			if u.PrincipalID != "" {
				subj["principalId"] = u.PrincipalID
			}
			raw, err := json.Marshal(subj)
			if err != nil {
				return fmt.Errorf("scim-identity-projection: marshal user %q: %w", u.SCIMID, err)
			}
			userBatch = append(userBatch, EntityUpsert{
				Kind:         "user",
				IdentityKeys: map[string]string{"identity.scimId": idp.Name + "/" + u.SCIMID},
				Labels:       map[string]string{"identity.name": u.UserName},
				Facets:       map[string]json.RawMessage{"identity.subject": raw},
			})
		}
		userIDs, err := proj.UpsertEntities(ctx, prov, userBatch)
		if err != nil {
			return fmt.Errorf("scim-identity-projection: upsert users %q: %w", idp.Name, err)
		}
		userEntityBySCIM := make(map[string]string, len(users))
		for i, u := range users {
			userEntityBySCIM[u.SCIMID] = userIDs[i]
		}

		// Groups → `group` Entities.
		groups, err := s.ListGroups(ctx, idp.Name, "", "")
		if err != nil {
			return fmt.Errorf("scim-identity-projection: list groups %q: %w", idp.Name, err)
		}
		groupBatch := make([]EntityUpsert, 0, len(groups))
		for _, g := range groups {
			subj := map[string]any{"scheme": "group", "name": g.DisplayName, "authority": idp.Name, "status": "active"}
			if g.ExternalID != "" {
				subj["externalId"] = g.ExternalID
			}
			raw, err := json.Marshal(subj)
			if err != nil {
				return fmt.Errorf("scim-identity-projection: marshal group %q: %w", g.SCIMID, err)
			}
			groupBatch = append(groupBatch, EntityUpsert{
				Kind:         "group",
				IdentityKeys: map[string]string{"identity.scimId": idp.Name + "/" + g.SCIMID},
				Labels:       map[string]string{"identity.name": g.DisplayName},
				Facets:       map[string]json.RawMessage{"identity.subject": raw},
			})
		}
		groupIDs, err := proj.UpsertEntities(ctx, prov, groupBatch)
		if err != nil {
			return fmt.Errorf("scim-identity-projection: upsert groups %q: %w", idp.Name, err)
		}

		// member-of Relations (user → group).
		for gi, g := range groups {
			members, err := s.GroupMembers(ctx, idp.Name, g.SCIMID)
			if err != nil {
				return fmt.Errorf("scim-identity-projection: members %q/%q: %w", idp.Name, g.SCIMID, err)
			}
			for _, memberSCIM := range members {
				uid, ok := userEntityBySCIM[memberSCIM]
				if !ok {
					continue // a member with no projected user (inactive/absent) — skip, not fatal
				}
				if err := proj.UpsertRelation(ctx, prov, "member-of", uid, groupIDs[gi]); err != nil {
					return fmt.Errorf("scim-identity-projection: member-of %s→%s: %w", uid, groupIDs[gi], err)
				}
			}
		}
	}
	return nil
}
