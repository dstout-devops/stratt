package controller

import (
	"context"
	"fmt"
)

// Enumerate reads the AWX estate the importer transforms. It fetches the
// top-level collections plus the per-object sub-resources the transform needs
// (survey specs, projects, workflow nodes, inventory sources/hosts). Read-only.
func (c *Client) Enumerate(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{
		WorkflowNodes:    map[int][]WorkflowNode{},
		Projects:         map[int]Project{},
		InventorySources: map[int][]InventorySource{},
		Hosts:            map[int][]Host{},
		Credentials:      map[int]Credential{},
		Surveys:          map[int]SurveySpec{},
	}

	var err error
	if snap.JobTemplates, err = list[JobTemplate](ctx, c, "/job_templates/"); err != nil {
		return nil, err
	}
	if snap.WorkflowJTs, err = list[WorkflowJobTemplate](ctx, c, "/workflow_job_templates/"); err != nil {
		return nil, err
	}
	if snap.Inventories, err = list[Inventory](ctx, c, "/inventories/"); err != nil {
		return nil, err
	}
	creds, err := list[Credential](ctx, c, "/credentials/")
	if err != nil {
		return nil, err
	}
	for _, cr := range creds {
		snap.Credentials[cr.ID] = cr
	}

	// Per job template: its project (SCM ref) and, if enabled, its survey.
	for _, jt := range snap.JobTemplates {
		if jt.Project != 0 {
			if _, ok := snap.Projects[jt.Project]; !ok {
				proj, err := get[Project](ctx, c, fmt.Sprintf("/projects/%d/", jt.Project))
				if err != nil {
					return nil, err
				}
				snap.Projects[jt.Project] = proj
			}
		}
		if jt.SurveyEnabled {
			spec, err := get[SurveySpec](ctx, c, fmt.Sprintf("/job_templates/%d/survey_spec/", jt.ID))
			if err != nil {
				return nil, err
			}
			if len(spec.Spec) > 0 {
				snap.Surveys[jt.ID] = spec
			}
		}
	}

	// Per workflow: its node graph.
	for _, wjt := range snap.WorkflowJTs {
		nodes, err := list[WorkflowNode](ctx, c, fmt.Sprintf("/workflow_job_templates/%d/workflow_nodes/", wjt.ID))
		if err != nil {
			return nil, err
		}
		snap.WorkflowNodes[wjt.ID] = nodes
	}

	// Per inventory: dynamic sources and manual hosts.
	for _, inv := range snap.Inventories {
		sources, err := list[InventorySource](ctx, c, fmt.Sprintf("/inventories/%d/inventory_sources/", inv.ID))
		if err != nil {
			return nil, err
		}
		snap.InventorySources[inv.ID] = sources
		hosts, err := list[Host](ctx, c, fmt.Sprintf("/inventories/%d/hosts/", inv.ID))
		if err != nil {
			return nil, err
		}
		snap.Hosts[inv.ID] = hosts
	}

	return snap, nil
}
