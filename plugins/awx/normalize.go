package awx

import (
	"encoding/json"
	"fmt"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Entity kinds + identity schemes this Connector projects. Kind == scheme == facet
// namespace per object type; the identity VALUE is controller-qualified so two
// Controllers never collide (§ cross-source identity). These are the `ansible.*`
// observed-projection Kinds — a foreign automation object mirrored read-only, never
// the frozen Stratt Named Kinds (Workflow/Trigger) those become on cutover.
const (
	KindTemplate = "ansible.template" // an AWX job template
	KindWorkflow = "ansible.workflow" // an AWX workflow job template
	KindSchedule = "ansible.schedule" // an AWX schedule (→ Trigger on cutover)
	KindOrg      = "ansible.org"      // an AWX organization (tenancy)
	KindTeam     = "ansible.team"     // an AWX RBAC team

	// schemePlaybook is the ansible-project Syncer's OWNED kind — referenced here only
	// as a cross-source relation TARGET (the `runs` edge), never owned or written by AWX.
	// Module isolation (ADR-0046) forbids importing that plugin's package, so the seam is
	// the contract STRING, not a shared Go symbol (the same way a SchemaId crosses modules).
	// AWX must be granted this as a pointable IdentityScheme (strattd), NOT a FacetNamespace.
	schemePlaybook = "ansible.playbook"
)

// qualify controller-namespaces an AWX object id: "<controllerID>/<id>".
func (c *Client) qualify(id int) string { return fmt.Sprintf("%s/%d", c.ctrlID, id) }

// Normalize maps a full Controller read into `ansible.*` ObservedEntities with the
// edges that make them a graph: a schedule → the object it launches, a team → its
// org, a template/workflow → its org. Read-only projection (§1.2): AWX stays the
// system-of-record and keeps executing; this is the always-on mirror.
func (c *Client) Normalize(snap *Snapshot) ([]*pluginv1.ObservedEntity, error) {
	out := make([]*pluginv1.ObservedEntity, 0, len(snap.JobTemplates)+len(snap.Workflows)+len(snap.Schedules)+len(snap.Organizations)+len(snap.Teams))

	orgRel := func(orgID int) []*pluginv1.ObservedRelation {
		if orgID == 0 {
			return nil
		}
		return []*pluginv1.ObservedRelation{{Type: "owned-by", ToScheme: KindOrg, ToValue: c.qualify(orgID)}}
	}

	for _, jt := range snap.JobTemplates {
		facet, err := json.Marshal(map[string]any{
			"name": jt.Name, "jobType": jt.JobType, "playbook": jt.Playbook,
			"surveyEnabled": jt.SurveyEnabled, "description": jt.Description,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindTemplate,
			IdentityKeys: map[string]string{KindTemplate: c.qualify(jt.ID)},
			Labels:       labels(jt.Name, jt.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindTemplate: facet},
			Relations:    append(orgRel(jt.SummaryFields.Organization.ID), runsRel(jt)...),
		})
	}

	for _, wf := range snap.Workflows {
		facet, err := json.Marshal(map[string]any{"name": wf.Name, "description": wf.Description})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindWorkflow,
			IdentityKeys: map[string]string{KindWorkflow: c.qualify(wf.ID)},
			Labels:       labels(wf.Name, wf.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindWorkflow: facet},
			Relations:    orgRel(wf.SummaryFields.Organization.ID),
		})
	}

	for _, sc := range snap.Schedules {
		facet, err := json.Marshal(map[string]any{
			"name": sc.Name, "rrule": sc.RRule, "enabled": sc.Enabled,
		})
		if err != nil {
			return nil, err
		}
		// The edge the graph exists for: a schedule launches its unified job template.
		// The target scheme is the KIND of the launched object (template vs workflow).
		var rels []*pluginv1.ObservedRelation
		if sc.UnifiedJobTemplate != 0 {
			scheme := KindTemplate
			if sc.SummaryFields.UnifiedJobTemplate.UnifiedJobType == "workflow_job_template" {
				scheme = KindWorkflow
			}
			rels = []*pluginv1.ObservedRelation{{Type: "schedules", ToScheme: scheme, ToValue: c.qualify(sc.UnifiedJobTemplate)}}
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindSchedule,
			IdentityKeys: map[string]string{KindSchedule: c.qualify(sc.ID)},
			Labels:       labels(sc.Name, ""),
			Facets:       map[string][]byte{KindSchedule: facet},
			Relations:    rels,
		})
	}

	for _, org := range snap.Organizations {
		facet, err := json.Marshal(map[string]any{"name": org.Name, "description": org.Description})
		if err != nil {
			return nil, err
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindOrg,
			IdentityKeys: map[string]string{KindOrg: c.qualify(org.ID)},
			Labels:       labels(org.Name, ""),
			Facets:       map[string][]byte{KindOrg: facet},
		})
	}

	for _, tm := range snap.Teams {
		facet, err := json.Marshal(map[string]any{"name": tm.Name})
		if err != nil {
			return nil, err
		}
		var rels []*pluginv1.ObservedRelation
		if tm.SummaryFields.Organization.ID != 0 {
			rels = []*pluginv1.ObservedRelation{{Type: "member-of", ToScheme: KindOrg, ToValue: c.qualify(tm.SummaryFields.Organization.ID)}}
		}
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         KindTeam,
			IdentityKeys: map[string]string{KindTeam: c.qualify(tm.ID)},
			Labels:       labels(tm.Name, tm.SummaryFields.Organization.Name),
			Facets:       map[string][]byte{KindTeam: facet},
			Relations:    rels,
		})
	}

	return out, nil
}

// runsRel is the CROSS-SOURCE edge that unifies AWX's orchestration layer with the raw
// Ansible content the ansible-project Syncer projects: `ansible.template --runs-->
// ansible.playbook`. The target is named BY IDENTITY as `<project.name>/<playbook>` —
// the same `<projectID>/<relpath>` key the ansible-project Syncer qualifies playbooks
// under, so the operator aligns STRATT_ANSIBLE_PROJECT_ID with the AWX Project name (the
// statement "this AWX Project IS this Git content root"). It is a SOFT reference: the
// host resolves it against the graph and, if the playbook is not projected by any known
// project, DROPS the edge (no vivify, §1.2) — that dropped edge is exactly the
// orphan-template signal the governance Baseline reads. No edge is emitted when AWX gives
// us neither a project name nor a playbook path (nothing to point at).
func runsRel(jt JobTemplate) []*pluginv1.ObservedRelation {
	proj, pb := jt.SummaryFields.Project.Name, jt.Playbook
	if proj == "" || pb == "" {
		return nil
	}
	return []*pluginv1.ObservedRelation{{Type: "runs", ToScheme: schemePlaybook, ToValue: proj + "/" + pb}}
}

// labels renders the operator-selectable labels: the object name and (when known)
// its owning org, so a View can group AWX content by name/org.
func labels(name, org string) map[string]string {
	m := map[string]string{}
	if name != "" {
		m["ansible.name"] = name
	}
	if org != "" {
		m["ansible.org"] = org
	}
	return m
}
