package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/plugins/ansible-automation/controller/awxapi/awxsim"
)

// ── awxsim's two halves, guarded rather than merged ───────────────────────────────────────────
//
// The Taskfile names "awxsim's two halves" as one of four divergent-second-copy defects this repo
// has paid for: the simulator defines its own fixture structs instead of sharing the client's.
//
// THE OBVIOUS FIX IS WRONG. awxsim models AWX'S WIRE, and two different readers decode SUBSETS of
// it — awxapi's adopt structs and this package's projection structs, which disagree on purpose
// (the projection reads run state and knobs the adopt path ignores; adopt reads the DAG shape the
// projection drops). A sim built from either client's type could only ever serve that client's
// view, so it could never catch the OTHER client's field going unserved. The duplication is
// structural, not laziness.
//
// What was actually missing is the guard. The failure mode is silent: add a field to a decode
// struct, forget the fixture, and every test still passes — the field is simply always zero, and
// the projection quietly reports nothing for it. This asserts the sim's own payload carries a key
// for every field the projection expects, which is the property sharing structs was meant to buy.

// jsonKeys returns the json tag names a struct decodes, recursing into embedded/nested structs
// only at the top level — enough to catch a field added beside its siblings.
func jsonKeys(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	return out
}

func TestTheSimServesEveryFieldTheProjectionDecodes(t *testing.T) {
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()
	sim.SetBase(srv.URL)

	// The wire, as the sim actually serves it — not as a struct claims it is.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v2/job_templates/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sim-token") // the sim gates every route
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Results) == 0 {
		t.Fatal("the sim served no job templates — this guard would pass vacuously")
	}

	// Union across the page: AWX omits nulls per object, so a field present on any row counts.
	served := map[string]bool{}
	for _, row := range page.Results {
		for k := range row {
			served[k] = true
		}
	}

	want := jsonKeys(reflect.TypeOf(JobTemplate{}))
	if len(want) < 10 {
		t.Fatalf("only %d decoded fields found — the reflection walk broke, so this asserts nothing", len(want))
	}
	for _, k := range want {
		if !served[k] {
			t.Errorf("the projection decodes %q and the sim never serves it: the field is silently "+
				"zero in every test, so a projection that reports nothing for it looks correct. "+
				"Add it to awxsim's fixture (this is the 'two halves' drift, guarded here rather "+
				"than fixed by sharing structs — see this file's header for why sharing is wrong)", k)
		}
	}
}
