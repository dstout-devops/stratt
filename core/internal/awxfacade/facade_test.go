package awxfacade

import (
	"encoding/base64"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// TestAWXIDParity pins the Go hash against the value the SQL twin
// graph.awx_run_id computes for the same uuid (verified: 1749380334). If either
// side drifts, job-id reverse lookup breaks — this is the guard.
func TestAWXIDParity(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	if got := awxID(uuid); got != 1749380334 {
		t.Fatalf("awxID(%s) = %d, want 1749380334 (SQL parity)", uuid, got)
	}
	if awxID("a") == awxID("b") {
		t.Fatal("distinct names must (almost always) hash distinctly")
	}
	if awxID("x") < 0 {
		t.Fatal("awx id must be a positive int31")
	}
}

func TestPaginateFilterOrderPage(t *testing.T) {
	mk := func(names ...string) []named {
		out := make([]named, len(names))
		for i, n := range names {
			out[i] = named{id: awxID(n), name: n, obj: map[string]any{"name": n}}
		}
		return out
	}

	// name__icontains filter.
	r := httptest.NewRequest("GET", "/api/v2/job_templates/?name__icontains=web", nil)
	env := paginate(r, mk("web-01", "db-01", "web-02"))
	if env.Count != 2 {
		t.Fatalf("icontains: count=%d want 2", env.Count)
	}

	// name__in filter.
	r = httptest.NewRequest("GET", "/api/v2/job_templates/?name__in=a,c", nil)
	env = paginate(r, mk("a", "b", "c"))
	if env.Count != 2 {
		t.Fatalf("__in: count=%d want 2", env.Count)
	}

	// paging + absolute next.
	r = httptest.NewRequest("GET", "http://awx.local/api/v2/jobs/?page_size=2", nil)
	r.Host = "awx.local"
	env = paginate(r, mk("a", "b", "c", "d", "e"))
	if env.Count != 5 || len(env.Results) != 2 {
		t.Fatalf("paging: count=%d results=%d", env.Count, len(env.Results))
	}
	if env.Next == nil || *env.Next != "http://awx.local/api/v2/jobs/?page=2&page_size=2" {
		t.Fatalf("next must be an absolute page-2 url, got %v", env.Next)
	}
	if env.Previous != nil {
		t.Fatal("page 1 has no previous")
	}

	// page_size clamps to max.
	r = httptest.NewRequest("GET", "/api/v2/jobs/?page_size=99999", nil)
	env = paginate(r, mk("a"))
	if env.Count != 1 {
		t.Fatalf("clamp: %d", env.Count)
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[types.RunStatus]string{
		types.RunPending:   "pending",
		types.RunRunning:   "running",
		types.RunSucceeded: "successful",
		types.RunFailed:    "failed",
		types.RunCanceled:  "canceled",
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%s)=%s want %s", in, got, want)
		}
	}
}

func TestWorkflowToJobTemplate(t *testing.T) {
	wf := types.Workflow{Name: "patch", Steps: []types.Step{{
		Name: "run", Actuator: "ansible", ViewName: "prod",
		Params: map[string]any{"scm": map[string]any{"playbook": "site.yml", "repo": "https://x/r.git"}},
	}}}
	step, ok := singleActuationStep(wf)
	if !ok {
		t.Fatal("expected a single actuation step")
	}
	jt := workflowToJobTemplate(wf, step)
	if jt["id"] != awxID("patch") || jt["name"] != "patch" || jt["playbook"] != "site.yml" {
		t.Fatalf("job_template fields: %+v", jt)
	}
	if jt["inventory"] != awxID("prod") {
		t.Fatalf("inventory id: %v", jt["inventory"])
	}
	if jt["ask_variables_on_launch"] != true {
		t.Fatal("must advertise ask_variables_on_launch for extra_vars")
	}
	rel := jt["related"].(map[string]any)
	if rel["launch"] != "/api/v2/job_templates/"+strconv.FormatInt(awxID("patch"), 10)+"/launch/" {
		t.Fatalf("launch related: %v", rel["launch"])
	}
}

func TestSingleActuationStepRejectsMultiAndGate(t *testing.T) {
	if _, ok := singleActuationStep(types.Workflow{Name: "w", Steps: []types.Step{{Name: "a"}, {Name: "b"}}}); ok {
		t.Error("multi-step must not be a job_template")
	}
	gate := types.Step{Name: "g", Gate: &types.GateSpec{}}
	if _, ok := singleActuationStep(types.Workflow{Name: "w", Steps: []types.Step{gate}}); ok {
		t.Error("gated workflow must not be a job_template")
	}
}

func TestRunToJob(t *testing.T) {
	start := time.Now().Add(-2 * time.Minute)
	fin := start.Add(90 * time.Second)
	run := types.Run{ID: "abc", Status: types.RunSucceeded, StartedAt: start, FinishedAt: &fin}
	job := runToJob(run)
	if job["status"] != "successful" || job["failed"] != false {
		t.Fatalf("status: %+v", job)
	}
	if job["job"] != awxID("abc") || job["id"] != awxID("abc") {
		t.Fatalf("id/job: %+v", job)
	}
	if e, _ := job["elapsed"].(float64); e < 89 || e > 91 {
		t.Fatalf("elapsed=%v want ~90", job["elapsed"])
	}
}

func TestViewToInventory(t *testing.T) {
	inv := viewToInventory(types.View{Name: "prod"}, 0)
	if inv["id"] != awxID("prod") || inv["total_hosts"] != 0 || inv["type"] != "inventory" {
		t.Fatalf("inventory: %+v", inv)
	}
}

func TestParseExtraVars(t *testing.T) {
	// object form
	obj, err := parseExtraVars([]byte(`{"msg":"hi","n":3}`))
	if err != nil || obj["msg"] != "hi" {
		t.Fatalf("object form: %v %v", obj, err)
	}
	// json string form
	s, err := parseExtraVars([]byte(`"{\"msg\":\"hi\"}"`))
	if err != nil || s["msg"] != "hi" {
		t.Fatalf("json-string form: %v %v", s, err)
	}
	// yaml string form
	y, err := parseExtraVars([]byte(`"msg: hey\nn: 2"`))
	if err != nil || y["msg"] != "hey" {
		t.Fatalf("yaml-string form: %v %v", y, err)
	}
	// null/empty
	if v, err := parseExtraVars([]byte(`null`)); err != nil || v != nil {
		t.Fatalf("null: %v %v", v, err)
	}
}

func TestBasicPassword(t *testing.T) {
	h := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:the-jwt-token"))
	pass, ok := basicPassword(h)
	if !ok || pass != "the-jwt-token" {
		t.Fatalf("basicPassword extracted %q ok=%v (want the-jwt-token)", pass, ok)
	}
	if _, ok := basicPassword("Basic !!!notbase64"); ok {
		t.Error("malformed base64 must fail")
	}
}
