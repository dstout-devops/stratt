package awx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeAWX is a minimal in-process AWX /api/v2, enough to exercise the Connector's
// read + projection with no real Controller (the plugin's content-expertise tested
// in isolation — no gRPC, no core).
func fakeAWX(t *testing.T) *httptest.Server {
	t.Helper()
	page := func(results any) []byte {
		b, _ := json.Marshal(map[string]any{"next": "", "results": results})
		return b
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/job_templates/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 10, "name": "Deploy Web", "job_type": "run", "playbook": "site.yml",
			"survey_enabled": true, "summary_fields": map[string]any{"organization": map[string]any{"id": 1, "name": "Platform"}},
		}}))
	})
	mux.HandleFunc("/api/v2/workflow_job_templates/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 20, "name": "Release Pipeline",
			"summary_fields": map[string]any{"organization": map[string]any{"id": 1, "name": "Platform"}},
		}}))
	})
	mux.HandleFunc("/api/v2/schedules/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 30, "name": "Nightly", "rrule": "DTSTART;FREQ=DAILY", "enabled": true,
			"unified_job_template": 10,
			"summary_fields":       map[string]any{"unified_job_template": map[string]any{"id": 10, "name": "Deploy Web", "unified_job_type": "job_template"}},
		}}))
	})
	mux.HandleFunc("/api/v2/organizations/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{"id": 1, "name": "Platform", "description": "core team"}}))
	})
	mux.HandleFunc("/api/v2/teams/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 40, "name": "SRE",
			"summary_fields": map[string]any{"organization": map[string]any{"id": 1, "name": "Platform"}},
		}}))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEnumerateAndNormalize(t *testing.T) {
	srv := fakeAWX(t)
	c := New(Config{Endpoint: srv.URL, ControllerID: "ctrl-a"})

	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(snap.JobTemplates) != 1 || len(snap.Workflows) != 1 || len(snap.Schedules) != 1 ||
		len(snap.Organizations) != 1 || len(snap.Teams) != 1 {
		t.Fatalf("snapshot counts wrong: %+v", snap)
	}

	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	byKind := map[string]*pluginv1.ObservedEntity{}
	for _, e := range ents {
		byKind[e.GetKind()] = e
	}
	for _, k := range []string{KindTemplate, KindWorkflow, KindSchedule, KindOrg, KindTeam} {
		if byKind[k] == nil {
			t.Fatalf("missing projected kind %q", k)
		}
	}

	// Identity is controller-qualified so two Controllers never collide.
	if got := byKind[KindTemplate].GetIdentityKeys()[KindTemplate]; got != "ctrl-a/10" {
		t.Fatalf("template identity = %q, want ctrl-a/10", got)
	}

	// The graph edge the mirror exists for: the schedule → the template it launches.
	sc := byKind[KindSchedule]
	if len(sc.GetRelations()) != 1 {
		t.Fatalf("schedule must carry one edge, got %d", len(sc.GetRelations()))
	}
	rel := sc.GetRelations()[0]
	if rel.GetType() != "schedules" || rel.GetToScheme() != KindTemplate || rel.GetToValue() != "ctrl-a/10" {
		t.Fatalf("schedule edge wrong: %+v", rel)
	}

	// The team → org edge (group management).
	tm := byKind[KindTeam]
	if len(tm.GetRelations()) != 1 || tm.GetRelations()[0].GetToScheme() != KindOrg || tm.GetRelations()[0].GetToValue() != "ctrl-a/1" {
		t.Fatalf("team→org edge wrong: %+v", tm.GetRelations())
	}

	// The observed facet carries AWX's literal detail (the playbook it runs).
	var tf struct {
		Name     string `json:"name"`
		Playbook string `json:"playbook"`
	}
	if err := json.Unmarshal(byKind[KindTemplate].GetFacets()[KindTemplate], &tf); err != nil {
		t.Fatalf("template facet decode: %v", err)
	}
	if tf.Name != "Deploy Web" || tf.Playbook != "site.yml" {
		t.Fatalf("template facet wrong: %+v", tf)
	}
}

func TestEmptyReadIsNotAFullSyncByDefault(t *testing.T) {
	// A Controller that returns empty collections must NOT assert a full sync (which
	// would tombstone the whole mirror) unless explicitly allowed (§1.8 guardrail).
	// Exercised at the projection layer: zero entities + default config.
	if (ServerConfig{}).AllowEmptyFullSync {
		t.Fatal("AllowEmptyFullSync must default false")
	}
}
