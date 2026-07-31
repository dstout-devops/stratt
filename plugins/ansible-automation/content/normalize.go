package content

import (
	"encoding/json"
	"path"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The `ansible.*` projection Kinds this Syncer emits — the PRIMITIVE half of the
// `ansible` domain (the AWX Connector projects the orchestration half). Kind == scheme
// == facet namespace per object type; the identity VALUE is project-qualified so two
// content roots never collide. These are observed foreign artifacts (a playbook file
// stays a playbook), never the frozen Stratt Named Kinds they become once ADOPTED
// (`stratt adopt` — the deliberate take-authority act; we never import, the projection is
// always-on). `ansible.playbook`/`ansible.inventory` echo two §2
// core-banned words, permissible ONLY because the `ansible.` prefix quarantines them as
// foreign-projection kinds (exactly as `ansible.template` renders AWX's "job template").
const (
	KindPlaybook   = "ansible.playbook"
	KindRole       = "ansible.role"
	KindCollection = "ansible.collection"
	KindInventory  = "ansible.inventory"
	// KindVarScope is a group_vars/host_vars binding site (ANS-003) — the largest thing an
	// Ansible repo holds that this projection could not see. SCOPE AND KEY NAMES ONLY, never
	// values: a vars file routinely holds credentials in the clear, which is why people vault
	// them, and §2.5 keeps material out of the graph. The names are what answer "why did this
	// host get this value"; the values are what must not be here to answer it.
	KindVarScope = "ansible.varscope"
)

// Normalize maps a full content-root read into read-only `ansible.*` ObservedEntities.
//
// It emits ONE relation family: `ansible.role --depends-on--> ansible.role`, from meta/main.yml
// (ANS-004). The cross-source edge `ansible.template --runs--> ansible.playbook` that unifies the
// two Sources lives on the controller half (a soft resolve-at-query reference, never a forcing FK)
// — and `depends-on` is soft in exactly the same way: it may name a role no Source has observed.
func (c *Client) Normalize(snap *Snapshot) ([]*pluginv1.ObservedEntity, error) {
	out := make([]*pluginv1.ObservedEntity, 0,
		len(snap.Playbooks)+len(snap.Roles)+len(snap.Collections)+len(snap.Inventories)+len(snap.VarScopes))

	emit := func(kind, id string, name string, facet map[string]any, rels ...*pluginv1.ObservedRelation) error {
		b, err := json.Marshal(facet)
		if err != nil {
			return err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         kind,
			IdentityKeys: map[string]string{kind: c.qualify(id)},
			Labels:       c.labels(name),
			Facets:       map[string][]byte{kind: b},
			Relations:    rels,
		})
		return nil
	}

	for _, pb := range snap.Playbooks {
		if err := emit(KindPlaybook, pb.Path, path.Base(pb.Path), map[string]any{
			"name": path.Base(pb.Path), "path": pb.Path, "plays": pb.Plays, "hosts": pb.Hosts,
		}); err != nil {
			return nil, err
		}
	}
	// Resolve a dependency NAME to the identity of the role it means. meta/main.yml names a
	// role, not a location, and that name may be an in-tree role OR a Galaxy one — so the edge
	// target cannot be computed from the name alone. Building the index first is what stops a
	// dependency on an in-tree role from dangling into the requirements space forever.
	roleIDByName := make(map[string]string, len(snap.Roles))
	for _, r := range snap.Roles {
		roleIDByName[r.Name] = roleID(r.Name, r.Path)
	}
	dependencyTarget := func(dep string) string {
		if id, ok := roleIDByName[dep]; ok {
			return id
		}
		// Named by a role and present in neither roles/ nor requirements.yml. It points into
		// the requirements space because that is what it IS — content that must be fetched —
		// and if it is later declared, the edge resolves to a real entity with no rewrite.
		return roleID(dep, "")
	}

	for _, r := range snap.Roles {
		facet := map[string]any{"name": r.Name, "path": r.Path, "required": r.Required}
		for k, v := range map[string]string{
			"version": r.Version, "source": r.Source, "author": r.Author,
			"license": r.License, "minAnsibleVersion": r.MinAnsibleVersion,
		} {
			if v != "" {
				facet[k] = v
			}
		}
		if len(r.Platforms) > 0 {
			facet["platforms"] = r.Platforms
		}
		if len(r.Dependencies) > 0 {
			facet["dependencies"] = r.Dependencies
		}
		// The role→role edges meta/main.yml declares (ANS-004). "What breaks if I change this
		// role" has no answer without them, and it is the first question a migration asks.
		//
		// An edge may point at a role NOT in this tree — a Galaxy role required but not yet
		// fetched, or one another project owns. That is a soft resolve-at-query reference and
		// is correct: refusing to draw it would hide a real dependency because the target has
		// not been observed yet, which is the §1.8 failure, not a referential-integrity win.
		rels := make([]*pluginv1.ObservedRelation, 0, len(r.Dependencies))
		for _, dep := range r.Dependencies {
			rels = append(rels, &pluginv1.ObservedRelation{
				Type: "depends-on", ToScheme: KindRole, ToValue: c.qualify(dependencyTarget(dep)),
			})
		}
		if err := emit(KindRole, roleID(r.Name, r.Path), r.Name, facet, rels...); err != nil {
			return nil, err
		}
	}
	for _, vs := range snap.VarScopes {
		if err := emit(KindVarScope, vs.Path, path.Base(vs.Path), map[string]any{
			"path": vs.Path, "scope": vs.Scope, "target": vs.Target,
			// Names, never values (§2.5). A VAULTED scope reports an empty list AND
			// vaulted:true — "binds nothing" and "binds things I cannot show you" are
			// different answers and must not render the same (§1.8).
			"keys": vs.Keys, "vaulted": vs.Vaulted,
		}); err != nil {
			return nil, err
		}
	}
	for _, col := range snap.Collections {
		if err := emit(KindCollection, col.Name, col.Name, map[string]any{
			"name": col.Name, "version": col.Version, "source": col.Source,
		}); err != nil {
			return nil, err
		}
	}
	for _, inv := range snap.Inventories {
		if err := emit(KindInventory, inv.Path, path.Base(inv.Path), map[string]any{
			"name": path.Base(inv.Path), "path": inv.Path, "format": inv.Format,
		}); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// roleID is a role's identity within this project, in one of TWO SPACES that must not collide.
//
// An IN-TREE role is identified by its PATH (`roles/apache`) — two roles can share a name across
// content roots, and the path is what actually distinguishes them. A REQUIRED role has no path and
// is identified by name under a distinct prefix (`requirements/geerlingguy.apache`).
//
// The prefixes differ deliberately. The first version of this used `"roles/" + name` for the
// required space, which collides exactly: an in-tree `roles/apache` and a requirements entry named
// `apache` produce the same identity, so one silently overwrites the other in the projection —
// the same entity asserted twice with different facets, and no error anywhere.
func roleID(name, rolePath string) string {
	if rolePath != "" {
		return rolePath
	}
	return "requirements/" + name
}

// labels renders the operator-selectable labels: the artifact's base name and the
// owning project, so a View can group ansible content by artifact or by project.
//
// `ansible.artifact` and NOT `ansible.name`, deliberately (ADR-0127, found by the
// two-Sources integration test). A label key has exactly ONE owner (ADR-0041,
// RegisterLabelOwner is ON CONFLICT (key) gated to the same owner_ref), so two Sources
// claiming `ansible.name` means the second half to register FAILS — which is what both
// halves did until this was fixed. The keys were never the same fact either: the
// controller half's `ansible.name` is an AAP object's name; this is a file's base name.
func (c *Client) labels(name string) map[string]string {
	m := map[string]string{"ansible.project": c.projectID}
	if name != "" {
		m["ansible.artifact"] = name
	}
	return m
}
