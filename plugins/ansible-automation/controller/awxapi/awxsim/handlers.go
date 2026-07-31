package awxsim

import (
	"net/http"
	"strconv"
)

func (s *Sim) jobTemplates(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/job_templates/", s.data.JobTemplates)
}

func (s *Sim) workflowJTs(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/workflow_job_templates/", s.data.WorkflowJTs)
}

func (s *Sim) schedules(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/schedules/", s.data.Schedules)
}

func (s *Sim) execEnvs(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/execution_environments/", s.data.ExecutionEnvs)
}

func (s *Sim) notifications(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/notification_templates/", s.data.Notifications)
}

func (s *Sim) labels(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/labels/", s.data.Labels)
}

func (s *Sim) users(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/users/", s.data.Users)
}

func (s *Sim) teamUsers(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	paged(s, w, r, "/api/v2/teams/"+strconv.Itoa(id)+"/users/", s.data.TeamMembers[id])
}

func (s *Sim) organizations(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/organizations/", s.data.Organizations)
}

func (s *Sim) teams(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/teams/", s.data.Teams)
}

func (s *Sim) inventories(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/inventories/", s.data.Inventories)
}

func (s *Sim) credentials(w http.ResponseWriter, r *http.Request) {
	paged(s, w, r, "/api/v2/credentials/", s.data.Credentials)
}

func (s *Sim) project(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, p := range s.data.Projects {
		if p.ID == id {
			writeJSON(w, p)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Sim) surveySpec(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if spec, ok := s.data.Surveys[id]; ok {
		writeJSON(w, spec)
		return
	}
	http.NotFound(w, r)
}

func (s *Sim) workflowNodes(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	paged(s, w, r, "/api/v2/workflow_job_templates/"+strconv.Itoa(id)+"/workflow_nodes/", s.data.WorkflowNodes[id])
}

func (s *Sim) inventorySources(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	paged(s, w, r, "/api/v2/inventories/"+strconv.Itoa(id)+"/inventory_sources/", s.data.InventorySources[id])
}

func (s *Sim) hosts(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	paged(s, w, r, "/api/v2/inventories/"+strconv.Itoa(id)+"/hosts/", s.data.Hosts[id])
}

// The single-object reads the ADR-0086 `adopt` deep-read uses (targeted, one object at a
// time) — distinct from the collection reads the one-shot importer enumerated.

func (s *Sim) jobTemplate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, jt := range s.data.JobTemplates {
		if jt.ID == id {
			writeJSON(w, jt)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Sim) credential(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, c := range s.data.Credentials {
		if c.ID == id {
			writeJSON(w, c)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Sim) inventory(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, inv := range s.data.Inventories {
		if inv.ID == id {
			writeJSON(w, inv)
			return
		}
	}
	http.NotFound(w, r)
}

func pathID(r *http.Request) int {
	n, _ := strconv.Atoi(r.PathValue("id"))
	return n
}
