package awxapi

import (
	"context"
	"fmt"
)

// Enumerate reads a whole AWX estate: the top-level collections plus the per-object
// sub-resources the transform needs (survey specs, projects, workflow nodes, inventory
// sources/hosts). Read-only.
//
// ── IT LOOKS DEAD AND IS NOT. READ THIS BEFORE DELETING IT ───────────────────────────────────
//
// No PRODUCTION path reaches Enumerate, and that is deliberate: ADR-0086 D1 refused the one-shot
// full-estate read outright — we never import, because the projection is always-on — and adopt goes
// through ReadJobTemplate instead. ADR-0089 D5 nonetheless kept this function, in as many words:
// "the rich client's Enumerate moves to the plugin and is retained there for a possible future
// bulk-adopt".
//
// It is also load-bearing TODAY, in a way the call graph hides. materialize/golden_test.go calls it
// to generate the golden CaC bundle, and core/internal/desiredstate/adopt_contract_test.go parses
// that bundle back across the module boundary — the §1.5 round-trip guarantee. Enumerate is what
// makes that fixture BROAD (several Views, Workflows and CredentialRefs); ReadJobTemplate reads one
// template and would collapse it to a happy-path subset.
//
// Narrowing this function is therefore caught by a breadth assertion in golden_test.go, and
// deliberately caught THERE rather than by the file-by-file drift comparison beside it: a narrowed
// emitter regenerated with -update is self-consistent, so drift alone would have stayed silent.
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
