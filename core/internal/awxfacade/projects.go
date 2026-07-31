package awxfacade

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/dstout-devops/stratt/types"
)

// ── projects: the AWX Projects resource, served from Actuator content roots ──────────────────────
//
// This one did NOT need inferring, which is why it is worth stating plainly: ADR-0134 D2 declares
// the mapping and even uses AWX's word for it. An Actuator's `contentDir` is "the TOOL-CONTENT root
// this Actuator runs — for ansible, one project: playbooks, roles/, group_vars/", and the ADR's rule
// is "ONE ACTUATOR PER PROJECT: a Step that needs different CONTENT names a different Actuator".
//
// So a project is not a synthesized grouping over Workflows — it is an Actuator with a content root,
// one-to-one. The alternative (deriving projects from distinct SCM blocks in Step params) would have
// been core reading a tool's params by name to invent an object, which is the §1.4 trap the ADR
// spends a paragraph warning implementers about.
//
// ── SCM TYPE IS "MANUAL", AND THAT IS THE TRUTH RATHER THAN A GAP ───────────────────────────────
// AWX projects either clone from SCM or point at a directory on disk (`scm_type: ""`, which AWX
// calls a manual project). Stratt's content is resolved at estate-PARSE time into the Actuator's
// spec and travels to Sites inside the JobSpec — nothing clones anything at run time, by design
// (ADR-0134 D3: dispatch stays filesystem-free). A manual project is therefore the FAITHFUL
// rendering, not a degraded one.
//
// `scm_revision` is empty, and the reason is booked rather than hidden: the estate is Git-declared,
// so a revision exists, but core does not track one per content root today (AWX-001). Reporting a
// plausible value — the daemon's build sha, say — would be inventing a fact about a repo. `status`
// is AWX's own "never updated", which is exactly what a manual project that has never run an SCM
// update reports there.
//
// READ-ONLY, and additionally there is no `POST /update/`: a project update means "clone the SCM
// again", and there is nothing here to clone. Offering one that no-ops would tell an operator their
// content refreshed when the only thing that refreshes it is an estate reconcile.

// actuatorToProject renders an Actuator's content root as an AWX project.
func actuatorToProject(a types.Actuator) map[string]any {
	id := awxID(a.Name)
	files := make([]string, 0, len(a.Content))
	for path := range a.Content {
		files = append(files, path)
	}
	sort.Strings(files) // stable output: a listing that reorders between calls reads as a change
	return map[string]any{
		"id":   id,
		"type": "project",
		"name": a.Name,
		"description": fmt.Sprintf("Stratt Actuator %q content root %q — %d files, resolved at estate "+
			"parse and carried in the JobSpec (nothing is cloned at run time)", a.Name, a.ContentDir, len(files)),
		"scm_type":   "", // manual: a directory, which is what a content root IS
		"scm_url":    "",
		"scm_branch": "",
		// Empty because core tracks no per-content-root revision yet (AWX-001), not because there
		// is none. A plausible substitute here would be a fact invented about a repo.
		"scm_revision": "",
		"local_path":   a.ContentDir,
		"status":       "never updated",
		"url":          jt("/api/v2/projects/%d/", id),
		"related": map[string]any{
			"playbooks": jt("/api/v2/projects/%d/playbooks/", id),
		},
		"summary_fields": map[string]any{
			"user_capabilities": map[string]bool{"edit": false, "delete": false, "start": false},
			"stratt": map[string]any{
				"actuator":      a.Name,
				"content_dir":   a.ContentDir,
				"files":         files,
				"write_ceiling": a.FacetNamespaces,
			},
		},
	}
}

// projectActuators returns the Actuators that HAVE a content root — the ones that are projects.
func (f *Facade) projectActuators(r *http.Request) ([]types.Actuator, error) {
	all, err := f.cfg.Store.ListActuators(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]types.Actuator, 0, len(all))
	for _, a := range all {
		if a.ContentDir != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// listProjects: GET /api/v2/projects/.
func (f *Facade) listProjects(w http.ResponseWriter, r *http.Request) {
	acts, err := f.projectActuators(r)
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(acts))
	for _, a := range acts {
		items = append(items, named{id: awxID(a.Name), name: a.Name, obj: actuatorToProject(a)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getProject: GET /api/v2/projects/{id}/.
func (f *Facade) getProject(w http.ResponseWriter, r *http.Request) {
	a, ok := f.projectByPathID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, actuatorToProject(a))
}

// getProjectPlaybooks: GET /api/v2/projects/{id}/playbooks/ — AWX returns a bare string array of
// the playbooks in the project, and awxkit uses it to validate a job_template's `playbook`.
//
// The filter is by EXTENSION and nothing else. Core does not parse play content (ADR-0117 D6 keeps
// tool awareness out of the runtime), so "which of these files is a playbook" cannot be answered
// here — only "which look like YAML at the root of the project", which is the same heuristic AWX's
// own listing degrades to and is honest about being.
func (f *Facade) getProjectPlaybooks(w http.ResponseWriter, r *http.Request) {
	a, ok := f.projectByPathID(w, r)
	if !ok {
		return
	}
	out := []string{}
	for path := range a.Content {
		if isRootYAML(path) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	writeJSON(w, http.StatusOK, out)
}

// isRootYAML reports a .yml/.yaml file at the project root (not inside roles/, group_vars/, …).
func isRootYAML(path string) bool {
	for i := range len(path) {
		if path[i] == '/' {
			return false
		}
	}
	n := len(path)
	return (n > 4 && path[n-4:] == ".yml") || (n > 5 && path[n-5:] == ".yaml")
}

func (f *Facade) projectByPathID(w http.ResponseWriter, r *http.Request) (types.Actuator, bool) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return types.Actuator{}, false
	}
	acts, err := f.projectActuators(r)
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return types.Actuator{}, false
	}
	for _, a := range acts {
		if awxID(a.Name) == id {
			return a, true
		}
	}
	awxErr(w, http.StatusNotFound, "Not found.")
	return types.Actuator{}, false
}

// projectIDs maps Actuator name → project id for the Actuators that have a content root, so a
// job_template can point at the project its Step's content comes from instead of reporting null.
// An Actuator with no content root gets NO entry: it has no project, and inventing one would make
// the id dangle.
func (f *Facade) projectIDs(r *http.Request) map[string]int64 {
	acts, err := f.projectActuators(r)
	if err != nil {
		return nil // a job_template with a null project is degraded, not wrong
	}
	out := make(map[string]int64, len(acts))
	for _, a := range acts {
		out[a.Name] = awxID(a.Name)
	}
	return out
}
