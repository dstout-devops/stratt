package controller

import (
	"context"
	"fmt"
)

// ReadJobTemplate performs the ADR-0086 model-(b) TARGETED deep-read: one AWX job template
// and exactly the sub-resources the transform needs FOR IT — its project (SCM ref), survey
// (if enabled), referenced credentials, and its inventory (+ dynamic sources and manual
// hosts) — producing a single-object Snapshot the (kept) importer transform consumes. It is
// NEVER a full-estate Enumerate: the object id is resolved from the live projection catalog
// and only its own definition is pulled. Read-only (§1.2); the token is supplied at
// invocation and never persisted (§2.5). A 404 on the template surfaces as an error so
// adopt fails loud on a gone object rather than emitting stale CaC.
func (c *Client) ReadJobTemplate(ctx context.Context, id int) (*Snapshot, error) {
	snap := &Snapshot{
		WorkflowNodes:    map[int][]WorkflowNode{},
		Projects:         map[int]Project{},
		InventorySources: map[int][]InventorySource{},
		Hosts:            map[int][]Host{},
		Credentials:      map[int]Credential{},
		Surveys:          map[int]SurveySpec{},
	}

	jt, err := get[JobTemplate](ctx, c, fmt.Sprintf("/job_templates/%d/", id))
	if err != nil {
		return nil, err
	}
	snap.JobTemplates = []JobTemplate{jt}

	if jt.Project != 0 {
		proj, err := get[Project](ctx, c, fmt.Sprintf("/projects/%d/", jt.Project))
		if err != nil {
			return nil, err
		}
		snap.Projects[jt.Project] = proj
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
	for _, cr := range jt.SummaryFields.Credentials {
		cred, err := get[Credential](ctx, c, fmt.Sprintf("/credentials/%d/", cr.ID))
		if err != nil {
			return nil, err
		}
		snap.Credentials[cred.ID] = cred
	}
	if jt.Inventory != 0 {
		inv, err := get[Inventory](ctx, c, fmt.Sprintf("/inventories/%d/", jt.Inventory))
		if err != nil {
			return nil, err
		}
		snap.Inventories = []Inventory{inv}
		if snap.InventorySources[inv.ID], err = list[InventorySource](ctx, c, fmt.Sprintf("/inventories/%d/inventory_sources/", inv.ID)); err != nil {
			return nil, err
		}
		if snap.Hosts[inv.ID], err = list[Host](ctx, c, fmt.Sprintf("/inventories/%d/hosts/", inv.ID)); err != nil {
			return nil, err
		}
	}
	return snap, nil
}
