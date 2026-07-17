package awximport

import (
	"fmt"
	"sort"

	"github.com/dstout-devops/stratt/core/internal/awximport/awx"
)

// mapJobTemplate transforms one AWX job template into a single-Step Workflow
// (the actuation tuple: ansible + scm content-ref + viewName + credentialRefs).
func mapJobTemplate(snap *awx.Snapshot, jt awx.JobTemplate, viewFor, credName map[int]string, name string, r *report) (string, error) {
	step := yStep{Name: "run", Actuator: "ansible"}

	view, ok := viewFor[jt.Inventory]
	if !ok || view == "" {
		r.block("Workflow %q (was: job template %q): no inventory resolved to a View — an actuation Step requires a View. Set viewName before apply.", name, jt.Name)
	}
	step.ViewName = view
	step.Params = actuationParams(snap, jt, r, name)

	for _, c := range jt.SummaryFields.Credentials {
		if n, ok := credName[c.ID]; ok {
			step.CredentialRefs = append(step.CredentialRefs, n)
		}
	}

	wf := yWorkflow{Name: name, Steps: []yStep{step}}
	doc, err := marshalYAML(wf)
	if err != nil {
		return "", mapErr("job template", jt.Name, err)
	}
	return doc, nil
}

// actuationParams builds the ansible Step params from a job template. A
// SCM-backed project yields a content-ref (scm); a manual project has no
// importable content, so it yields a valid placeholder `play` (which round-trips
// and runs harmlessly) plus a blocking report entry to supply real content.
func actuationParams(snap *awx.Snapshot, jt awx.JobTemplate, r *report, wfName string) map[string]any {
	proj, ok := snap.Projects[jt.Project]
	if !ok || proj.ScmType == "" || proj.ScmURL == "" {
		r.block("Workflow %q (was: job template %q, playbook %q): its project is not SCM-backed, so the playbook content is not available to import. The Step carries a placeholder play — replace it with an scm content-ref (repo + playbook) or the real inline play before apply.", wfName, jt.Name, jt.Playbook)
		return map[string]any{"play": placeholderPlay(jt)}
	}
	scm := map[string]any{"repo": proj.ScmURL, "playbook": jt.Playbook}
	if proj.ScmBranch != "" {
		scm["ref"] = proj.ScmBranch
	}
	params := map[string]any{"scm": scm}
	if jt.JobType == "check" {
		params["check"] = true
	}
	return params
}

// placeholderPlay is a valid (round-tripping) play that does nothing but flag
// the migration TODO — used when a job template's content is not importable.
func placeholderPlay(jt awx.JobTemplate) string {
	return "- hosts: all\n" +
		"  tasks:\n" +
		"    - name: MIGRATION TODO — no importable SCM content for '" + jt.Playbook + "'\n" +
		"      ansible.builtin.debug:\n" +
		"        msg: \"Replace this Step's params with an scm content-ref or the real play.\"\n"
}

// mapWorkflow transforms an AWX workflow job template and its node graph into a
// multi-Step Workflow: node edges → needs+when; approval nodes → Gates.
func mapWorkflow(snap *awx.Snapshot, wjt awx.WorkflowJobTemplate, viewFor, credName map[int]string, name string, r *report) (string, error) {
	nodes := snap.WorkflowNodes[wjt.ID]
	byID := map[int]awx.WorkflowNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	// Collect, per child node, the conditions under which each parent reaches
	// it. A child reached under one condition → one Step with that `when`; a
	// child reached under multiple conditions from different parents → one
	// Step copy per condition (a single `when` cannot express a mix).
	type edge struct{ parent, when string }
	incoming := map[int][]edge{}
	for _, n := range nodes {
		parent := stepName(n)
		for _, c := range n.SuccessNodes {
			incoming[c] = append(incoming[c], edge{parent, "success"})
		}
		for _, c := range n.FailureNodes {
			incoming[c] = append(incoming[c], edge{parent, "failure"})
		}
		for _, c := range n.AlwaysNodes {
			incoming[c] = append(incoming[c], edge{parent, "always"})
		}
	}

	var steps []yStep
	for _, n := range sortedNodes(nodes) {
		base := buildNode(snap, n, viewFor, credName, name, r)
		conds := incoming[n.ID]
		switch {
		case len(conds) == 0:
			steps = append(steps, base) // a root node — no needs/when
		default:
			byWhen := map[string][]string{}
			for _, e := range conds {
				byWhen[e.when] = append(byWhen[e.when], e.parent)
			}
			if len(byWhen) > 1 {
				r.note("Workflow %q: Step %q is reached under multiple conditions (%s); emitted one Step copy per condition.", name, base.Name, joinKeys(byWhen))
			}
			for _, when := range sortedKeys(byWhen) {
				s := base
				s.Needs = dedupSorted(byWhen[when])
				s.When = when
				if len(byWhen) > 1 {
					s.Name = base.Name + "-" + when
				}
				steps = append(steps, s)
			}
		}
	}

	if len(steps) == 0 {
		r.block("Workflow %q (was: workflow job template %q): it has no nodes — nothing to run.", name, wjt.Name)
		steps = append(steps, yStep{Name: "noop", Gate: &yGate{Approvers: yApprovers{Teams: []string{"REVIEW-ME"}}}})
	}

	wf := yWorkflow{Name: name, Steps: steps}
	doc, err := marshalYAML(wf)
	if err != nil {
		return "", mapErr("workflow job template", wjt.Name, err)
	}
	return doc, nil
}

// buildNode renders one workflow node as a Step (actuation or Gate), without
// its edge-derived needs/when (the caller fills those).
func buildNode(snap *awx.Snapshot, n awx.WorkflowNode, viewFor, credName map[int]string, wfName string, r *report) yStep {
	if n.SummaryFields.UnifiedJobTemplate.UnifiedJobType == "workflow_approval" {
		g := &yGate{Approvers: yApprovers{Teams: []string{"REVIEW-ME"}}, TimeoutSeconds: n.Timeout}
		r.block("Workflow %q: Gate Step %q (was: approval node) has no approver identity in AWX — set gate.approvers before apply.", wfName, stepName(n))
		return yStep{Name: stepName(n), Gate: g}
	}

	jtID := n.UnifiedJobTemplate
	jt, ok := findJT(snap, jtID)
	if !ok {
		// A non-job-template node (e.g. a nested workflow or project sync):
		// not imported as an actuation in v1. Emit a manual-review Gate so the
		// DAG stays intact and the operator must replace it.
		r.block("Workflow %q: node %q references a unified job template (id %d) that is not a job template — nested workflows / project syncs are not imported as Steps in v1. Emitted a review Gate in its place.", wfName, stepName(n), jtID)
		return yStep{Name: stepName(n), Gate: &yGate{Approvers: yApprovers{Teams: []string{"REVIEW-ME"}}}}
	}
	step := yStep{Name: stepName(n), Actuator: "ansible"}
	step.ViewName = viewFor[jt.Inventory]
	step.Params = actuationParams(snap, jt, r, wfName)
	for _, c := range jt.SummaryFields.Credentials {
		if nm, ok := credName[c.ID]; ok {
			step.CredentialRefs = append(step.CredentialRefs, nm)
		}
	}
	return step
}

func stepName(n awx.WorkflowNode) string {
	if n.Identifier != "" {
		return slug(n.Identifier)
	}
	return fmt.Sprintf("n%d", n.ID)
}

func findJT(snap *awx.Snapshot, id int) (awx.JobTemplate, bool) {
	for _, jt := range snap.JobTemplates {
		if jt.ID == id {
			return jt, true
		}
	}
	return awx.JobTemplate{}, false
}

func sortedNodes(nodes []awx.WorkflowNode) []awx.WorkflowNode {
	out := append([]awx.WorkflowNode(nil), nodes...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinKeys(m map[string][]string) string {
	return fmt.Sprint(sortedKeys(m))
}
