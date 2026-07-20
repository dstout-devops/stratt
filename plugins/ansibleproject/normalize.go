package ansibleproject

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
)

// Normalize maps a full content-root read into read-only `ansible.*` ObservedEntities.
// This slice projects the artifacts flat (no relations); the cross-source edge
// `ansible.template --runs--> ansible.playbook` that unifies the two Sources is a
// deliberate next slice (a soft resolve-at-query reference, never a forcing FK).
func (c *Client) Normalize(snap *Snapshot) ([]*pluginv1.ObservedEntity, error) {
	out := make([]*pluginv1.ObservedEntity, 0, len(snap.Playbooks)+len(snap.Roles)+len(snap.Collections)+len(snap.Inventories))

	emit := func(kind, id string, name string, facet map[string]any) error {
		b, err := json.Marshal(facet)
		if err != nil {
			return err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         kind,
			IdentityKeys: map[string]string{kind: c.qualify(id)},
			Labels:       c.labels(name),
			Facets:       map[string][]byte{kind: b},
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
	for _, r := range snap.Roles {
		if err := emit(KindRole, r.Path, r.Name, map[string]any{
			"name": r.Name, "path": r.Path,
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

// labels renders the operator-selectable labels: the artifact's base name and the
// owning project, so a View can group ansible content by name or by project.
func (c *Client) labels(name string) map[string]string {
	m := map[string]string{"ansible.project": c.projectID}
	if name != "" {
		m["ansible.name"] = name
	}
	return m
}
