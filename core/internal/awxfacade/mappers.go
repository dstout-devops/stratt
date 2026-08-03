package awxfacade

import (
	"fmt"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// mapStatus translates a Stratt Run status to AWX's job status vocabulary.
// AWX new/waiting/error have no Stratt equivalent.
func mapStatus(s types.RunStatus) string {
	switch s {
	case types.RunSucceeded:
		return "successful"
	case types.RunFailed, types.RunPartial:
		// AWX has no "partial" — a cross-Cell Run that failed in some region
		// (ADR-0044 slice 5) reads as failed on the compat surface, never a
		// false "successful" (§1.8). The full per-Cell breakdown lives on the
		// native Run.
		return "failed"
	case types.RunCanceled:
		return "canceled"
	case types.RunRunning:
		return "running"
	default:
		return "pending"
	}
}

// singleActuationStep returns a Workflow's sole actuation Step when it has
// exactly one (a Gate-free single-Step Workflow) — the shape presented as an
// AWX job_template. Multi-Step / gated Workflows are not job_templates in v1.
func singleActuationStep(wf types.Workflow) (types.Step, bool) {
	var found types.Step
	n := 0
	for _, s := range wf.Steps {
		if s.Gate != nil {
			return types.Step{}, false
		}
		found = s
		n++
	}
	if n != 1 {
		return types.Step{}, false
	}
	return found, true
}

// scmField digs params.scm.<key> (the ansible content-ref shape).
func scmField(params map[string]any, key string) string {
	scm, ok := params["scm"].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := scm[key].(string)
	return v
}

// viewToInventory renders a View as an AWX inventory.
func viewToInventory(view types.View, totalHosts int) map[string]any {
	id := awxID(view.Name)
	return map[string]any{
		"id":          id,
		"type":        "inventory",
		"name":        view.Name,
		"kind":        "",
		"total_hosts": totalHosts,
		"url":         jt("/api/v2/inventories/%d/", id),
		"related":     map[string]any{"hosts": jt("/api/v2/inventories/%d/hosts/", id)},
		"summary_fields": map[string]any{
			"user_capabilities": map[string]bool{"edit": false, "delete": false},
		},
	}
}

// workflowToJobTemplate renders a single-Step Workflow as an AWX job_template.
//
// projects maps Actuator name → project id (projects.go). A Step whose Actuator declares a content
// root gets a real `project`; one that does not gets null, because it genuinely has no project and
// a synthesized id would dangle.
func workflowToJobTemplate(wf types.Workflow, step types.Step, projects map[string]int64) map[string]any {
	id := awxID(wf.Name)
	invID := awxID(step.ViewName)
	// Which file an AWX client sees as the job template's `playbook`, in the order the three
	// content sources were added: a mounted-project ref (ADR-0134), an SCM clone (ADR-0025),
	// or the inline play the shim writes to project/play.yml.
	//
	// Reading a tool's param key BY NAME is legitimate here and nowhere else in core: this
	// package's entire job is to render Stratt as AWX, so it is already tool-specific by
	// construction. The spine's mount path stays declaration-driven (contentInputs) — see
	// desiredstate.validateContentRefs, which is the same question asked content-blind.
	playbook, _ := step.Params["playbook"].(string)
	if playbook == "" {
		playbook = scmField(step.Params, "playbook")
	}
	if playbook == "" {
		playbook = "play.yml" // inline-play Workflows have no content ref at all
	}
	var project any
	if pid, ok := projects[step.Actuator]; ok {
		project = pid
	}
	out := map[string]any{
		"id":        id,
		"type":      "job_template",
		"name":      wf.Name,
		"job_type":  "run",
		"inventory": invID,
		"project":   project,
		"playbook":  playbook,
		"url":       jt("/api/v2/job_templates/%d/", id),
		"related": map[string]any{
			"launch":    jt("/api/v2/job_templates/%d/launch/", id),
			"inventory": jt("/api/v2/inventories/%d/", invID),
			"jobs":      jt("/api/v2/job_templates/%d/jobs/", id),
		},
		"summary_fields": map[string]any{
			"inventory":         map[string]any{"id": invID, "name": step.ViewName},
			"user_capabilities": map[string]bool{"start": true, "edit": false, "delete": false},
		},
	}
	if project != nil {
		out["related"].(map[string]any)["project"] = jt("/api/v2/projects/%d/", project)
		out["summary_fields"].(map[string]any)["project"] =
			map[string]any{"id": project, "name": step.Actuator}
	}
	// The prompts, DERIVED from what this Workflow declares and this Step binds (ADR-0160 D2) rather
	// than hardcoded. Three booleans used to live here, two of them permanently false, and AWX
	// tooling reads them to decide what to prompt for — so a migrated template silently lost prompts
	// the mechanism already supported.
	// true unconditionally on THIS surface: the job_template launch path merges untyped extra_vars
	// when the Workflow declares no inputs, so the door accepts them either way. See askFields.
	for k, v := range askFields(wf, step, true) {
		out[k] = v
	}
	return out
}

// runToJob renders a Run as an AWX job.
func runToJob(run types.Run) map[string]any {
	id := awxID(run.ID)
	status := mapStatus(run.Status)
	var finished any
	elapsed := 0.0
	if run.FinishedAt != nil {
		finished = run.FinishedAt.UTC().Format(time.RFC3339)
		elapsed = run.FinishedAt.Sub(run.StartedAt).Seconds()
	} else if !run.StartedAt.IsZero() {
		elapsed = time.Since(run.StartedAt).Seconds()
	}
	job := map[string]any{
		"id":       id,
		"job":      id,
		"type":     "job",
		"status":   status,
		"failed":   run.Status == types.RunFailed || run.Status == types.RunPartial,
		"started":  run.StartedAt.UTC().Format(time.RFC3339),
		"finished": finished,
		"elapsed":  elapsed,
		"url":      jt("/api/v2/jobs/%d/", id),
		"related": map[string]any{
			"stdout": jt("/api/v2/jobs/%d/stdout/", id),
			"cancel": jt("/api/v2/jobs/%d/cancel/", id),
		},
	}
	return job
}

// jt formats a relative AWX resource path (AWX url/related fields are
// root-relative, e.g. "/api/v2/jobs/72/").
func jt(format string, a ...any) string {
	return fmt.Sprintf(format, a...)
}
