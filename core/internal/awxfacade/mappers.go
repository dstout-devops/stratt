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
	case types.RunFailed:
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
func workflowToJobTemplate(wf types.Workflow, step types.Step) map[string]any {
	id := awxID(wf.Name)
	invID := awxID(step.ViewName)
	playbook := scmField(step.Params, "playbook")
	if playbook == "" {
		playbook = "play.yml" // inline-play Workflows have no SCM path
	}
	return map[string]any{
		"id":                      id,
		"type":                    "job_template",
		"name":                    wf.Name,
		"job_type":                "run",
		"inventory":               invID,
		"project":                 nil,
		"playbook":                playbook,
		"ask_variables_on_launch": true,
		"ask_limit_on_launch":     false,
		"ask_inventory_on_launch": false,
		"url":                     jt("/api/v2/job_templates/%d/", id),
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
		"failed":   run.Status == types.RunFailed,
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
