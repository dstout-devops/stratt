package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// unambiguousLoginKeys decides which identities get the pointable `identity.userName` key
// (ADR-0155 D1), returning SCIM id → key for those that do.
//
// A `user` Entity gains that key as a SECOND way to be ADDRESSED, so a foreign projection that
// knows a person only by the name they log in with can POINT at them — which is what lets the AWX
// mirror ask "does this local account match anybody the IdP knows?" without claiming an identity
// fact it may not write (§2.1).
//
// THE KEY IS EMITTED ONLY WHEN THE NAME IS UNAMBIGUOUS, and this projector is the only component
// that can decide that: it enumerates every IdP in one pass, so it alone can see that `jsmith`
// exists in two directories. When it does, NEITHER entity gets the key and nothing links — which is
// the correct answer rather than a gap. Two candidate people is not a person, and picking one would
// be the implicit precedence §2.4 refuses outright.
//
// Lowercased, and that is measured rather than assumed: RFC 7643 §4.1.1 defines SCIM userName as
// unique across the provider's Users with caseExact:false, so two identities cannot differ only by
// case — lowercasing cannot merge two distinct people, and it makes the join robust against a
// Controller storing `JSmith` where the IdP stores `jsmith`.
//
// A pure function on purpose: the graph tests are Postgres-gated and SKIP without a database, which
// is exactly how an inert mechanism stays green in this repo. This rule is the subtle part, so it is
// testable without one.
func unambiguousLoginKeys(usersByIDP map[string][]types.SCIMIdentity) map[string]string {
	claimants := map[string]map[string]bool{}
	for idp, users := range usersByIDP {
		for _, u := range users {
			key := strings.ToLower(strings.TrimSpace(u.UserName))
			if key == "" {
				continue
			}
			if claimants[key] == nil {
				claimants[key] = map[string]bool{}
			}
			claimants[key][idp] = true
		}
	}
	out := map[string]string{}
	for _, users := range usersByIDP {
		for _, u := range users {
			key := strings.ToLower(strings.TrimSpace(u.UserName))
			if key == "" || len(claimants[key]) != 1 {
				continue
			}
			out[u.SCIMID] = key
		}
	}
	return out
}

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
	// The namespace comes from types rather than a literal here, because the estate loader must
	// agree that it is owned: a `facetWriteScope` naming a namespace no declaration claims is
	// refused at load, and this one is claimed by nothing an estate can declare. One list, two
	// readers — a second copy would let the check and this projector disagree.
	for _, ns := range types.ProjectorOwnedFacetNamespaces() {
		if err := s.RegisterFacetOwner(ctx, types.FacetOwner{
			Namespace: ns,
			OwnerKind: string(types.WriterSyncer),
			OwnerRef:  scimIdentityProjector,
		}); err != nil {
			return err
		}
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

	// The pointable login-name keys (ADR-0155 D1). Every IdP's users are read ONCE here and
	// reused below, both because the ambiguity decision needs to see all of them at once and
	// because reading them twice would double this projection's cost for nothing.
	usersByIDP := make(map[string][]types.SCIMIdentity, len(idps))
	for _, idp := range idps {
		users, err := s.ListIdentities(ctx, idp.Name, "", "")
		if err != nil {
			return fmt.Errorf("scim-identity-projection: list identities %q: %w", idp.Name, err)
		}
		usersByIDP[idp.Name] = users
	}
	loginKeys := unambiguousLoginKeys(usersByIDP)

	for _, idp := range idps {
		src, err := s.RegisterSource(ctx, types.Source{Kind: "scim", Name: "scim:" + idp.Name})
		if err != nil {
			return fmt.Errorf("scim-identity-projection: register source %q: %w", idp.Name, err)
		}
		prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: scimIdentityProjector, SourceID: src.ID}

		// Users → `user` Entities. Read in the unambiguity pass above, not again here.
		users := usersByIDP[idp.Name]
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
			keys := map[string]string{"identity.scimId": idp.Name + "/" + u.SCIMID}
			// The pointable login-name key (ADR-0155 D1) — a second way to ADDRESS an entity
			// this projector already owns, never a second claim about the person. Absent when
			// the name is ambiguous across IdPs.
			if k, ok := loginKeys[u.SCIMID]; ok {
				keys["identity.userName"] = k
			}
			userBatch = append(userBatch, EntityUpsert{
				Kind:         "user",
				IdentityKeys: keys,
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
