package controller

// The projection half run against awxsim — the shared dev/test stand-in — rather than a
// bespoke in-file fake. This exists because of an asymmetry the parity audit found and
// that had been sitting in the harness itself: awxsim served ONLY the adopt deep-read's
// endpoints, so the Syncer could never run against it, and the two halves of this module
// were tested by two different simulators of different breadth.
//
// That matters beyond tidiness. `Enumerate` fails the whole Observe on any one
// collection's error (an empty projection must never be presented as a successful full
// sync, §1.8), so a sim missing /schedules/, /organizations/ and /teams/ meant the
// projection path was not merely untested against it — it was unrunnable.

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/plugins/ansible-automation/controller/awxapi/awxsim"
)

func simClient(t *testing.T) *Client {
	t.Helper()
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	sim.SetBase(srv.URL)
	return New(Config{Endpoint: srv.URL, Token: "sim-token", ControllerID: "ctrl-a"})
}

// The full projection against the shared sim: every collection enumerates (including the
// three that only this half reads), paging works on a projection-only endpoint, and the
// snapshot normalizes into the ten `ansible.*` entities the sim's estate describes.
func TestProjectionAgainstAwxsim(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate against awxsim: %v", err)
	}

	for _, tc := range []struct {
		what string
		got  int
		want int
	}{
		{"job templates", len(snap.JobTemplates), 2},
		{"workflows", len(snap.Workflows), 1},
		{"schedules", len(snap.Schedules), 3}, // 3 > pageSize: the `next` cursor is followed
		{"organizations", len(snap.Organizations), 2},
		{"teams", len(snap.Teams), 2},
	} {
		if tc.got != tc.want {
			t.Errorf("%s enumerated = %d, want %d", tc.what, tc.got, tc.want)
		}
	}

	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(ents) != 10 {
		t.Fatalf("projected %d entities, want 10 (2 templates + 1 workflow + 3 schedules + 2 orgs + 2 teams)", len(ents))
	}

	byKind := map[string]int{}
	for _, e := range ents {
		byKind[e.GetKind()]++
	}
	for kind, want := range map[string]int{
		KindTemplate: 2, KindWorkflow: 1, KindSchedule: 3, KindOrg: 2, KindTeam: 2,
	} {
		if byKind[kind] != want {
			t.Errorf("projected %d %s, want %d", byKind[kind], kind, want)
		}
	}
}

// The edges are the reason the projection is a graph rather than a list. Asserted
// against the sim's estate: every template/workflow owned-by its org, every team
// member-of its org, each schedule pointing at what it launches — with the target SCHEME
// switching on unified_job_type — and the cross-source `runs` edge naming a playbook
// this Source must never own.
func TestProjectionEdgesAgainstAwxsim(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	type edge struct{ typ, scheme, value string }
	edges := map[string][]edge{} // identity value -> its outbound edges
	for _, e := range ents {
		id := e.GetIdentityKeys()[e.GetKind()]
		for _, r := range e.GetRelations() {
			edges[id] = append(edges[id], edge{r.GetType(), r.GetToScheme(), r.GetToValue()})
		}
	}

	has := func(from string, want edge) bool {
		for _, g := range edges[from] {
			if g == want {
				return true
			}
		}
		return false
	}

	// The cross-source edge (ADR-0085): <project.name>/<playbook>, onto a scheme the
	// content half owns. Dropped by the host when unresolvable — that drop is the signal.
	if !has("ctrl-a/10", edge{"runs", "ansible.playbook", "infra/site.yml"}) {
		t.Errorf("template 10 has no `runs` edge onto infra/site.yml; got %v", edges["ctrl-a/10"])
	}
	if !has("ctrl-a/11", edge{"runs", "ansible.playbook", "local-scripts/facts.yml"}) {
		t.Errorf("template 11 has no `runs` edge onto local-scripts/facts.yml; got %v", edges["ctrl-a/11"])
	}
	// Tenancy.
	if !has("ctrl-a/10", edge{"owned-by", KindOrg, "ctrl-a/1"}) {
		t.Errorf("template 10 is not owned-by org 1; got %v", edges["ctrl-a/10"])
	}
	if !has("ctrl-a/20", edge{"owned-by", KindOrg, "ctrl-a/1"}) {
		t.Errorf("workflow 20 is not owned-by org 1; got %v", edges["ctrl-a/20"])
	}
	if !has("ctrl-a/1", edge{"member-of", KindOrg, "ctrl-a/1"}) {
		t.Errorf("team 1 is not member-of org 1; got %v", edges["ctrl-a/1"])
	}
	// A schedule's target SCHEME follows unified_job_type — 30 launches a job template,
	// 31 launches a workflow. Getting this wrong points the edge at a scheme whose
	// identity does not exist, and the host silently drops it.
	if !has("ctrl-a/30", edge{"schedules", KindTemplate, "ctrl-a/10"}) {
		t.Errorf("schedule 30 does not target template 10; got %v", edges["ctrl-a/30"])
	}
	if !has("ctrl-a/31", edge{"schedules", KindWorkflow, "ctrl-a/20"}) {
		t.Errorf("schedule 31 does not target WORKFLOW 20; got %v", edges["ctrl-a/31"])
	}
}

// The disabled schedule survives the projection as disabled — it is the dead-automation
// case the awx-schedule-enabled Baseline reads, and `enabled` is one of the three fields
// the PINNED ansible.schedule Facet schema carries.
func TestDisabledScheduleProjectsAsDisabled(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var found bool
	for _, e := range ents {
		if e.GetKind() != KindSchedule || e.GetIdentityKeys()[KindSchedule] != "ctrl-a/32" {
			continue
		}
		found = true
		if got := string(e.GetFacets()[KindSchedule]); !contains(got, `"enabled":false`) {
			t.Errorf("retired schedule facet = %s, want enabled:false", got)
		}
	}
	if !found {
		t.Fatal("the disabled schedule was not projected at all")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
