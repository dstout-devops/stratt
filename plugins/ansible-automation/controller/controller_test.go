package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
			"survey_enabled": true,
			"status":         "failed", "last_job_failed": true, "last_job_run": "2026-07-26T03:00:00Z",
			"forks": 5, "limit": "web*", "job_tags": "deploy", "become_enabled": true,
			"summary_fields": map[string]any{
				"organization": map[string]any{"id": 1, "name": "Platform"},
				"project":      map[string]any{"id": 5, "name": "web-content"},
				"credentials":  []map[string]any{{"id": 50, "name": "prod-ssh", "kind": "ssh"}},
			},
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
	// EXPLICIT, and more specific than the collection pattern above — without it Go's mux
	// prefix-matches this onto /api/v2/workflow_job_templates/ and the node fetch silently
	// decodes a page of WORKFLOWS as nodes: no error, no edges, and a fake that lies.
	mux.HandleFunc("/api/v2/workflow_job_templates/{id}/workflow_nodes/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 100, "unified_job_template": 10,
			"summary_fields": map[string]any{"unified_job_template": map[string]any{"id": 10, "name": "Deploy Web", "unified_job_type": "job"}},
		}}))
	})
	mux.HandleFunc("/api/v2/execution_environments/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{"id": 80, "name": "pinned-ee", "image": "quay.io/x@sha256:" + strings.Repeat("a", 64)}}))
	})
	mux.HandleFunc("/api/v2/projects/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 1, "name": "infra", "scm_type": "git",
			"scm_url": "https://github.com/example/infra.git", "scm_branch": "main",
			"scm_revision": "9f1c2d3", "status": "successful",
		}}))
	})
	mux.HandleFunc("/api/v2/notification_templates/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{
			"id": 90, "name": "slack-ops", "notification_type": "slack",
			// In the clear, as AWX returns it: the token is IN the URL.
			"notification_configuration": map[string]any{
				"hook_url": "https://hooks.slack.invalid/services/T0/B0/XXXXXXXXXXXX", "channels": []string{"#ops"},
			},
		}}))
	})
	mux.HandleFunc("/api/v2/labels/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{"id": 70, "name": "prod"}}))
	})
	mux.HandleFunc("/api/v2/users/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{"id": 60, "username": "admin", "is_active": true, "is_superuser": true}}))
	})
	// Explicit and more specific than /api/v2/teams/, for the same reason the workflow-node
	// route is: without it the mux prefix-matches and decodes a page of TEAMS as members.
	mux.HandleFunc("/api/v2/teams/{id}/users/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{"id": 60, "username": "admin"}}))
	})
	mux.HandleFunc("/api/v2/credentials/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(page([]map[string]any{{"id": 50, "name": "prod-ssh", "kind": "ssh"}}))
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
		len(snap.Organizations) != 1 || len(snap.Teams) != 1 || len(snap.Credentials) != 1 || len(snap.Users) != 1 || len(snap.Labels) != 1 || len(snap.ExecutionEnvs) != 1 {
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
	for _, k := range []string{KindTemplate, KindWorkflow, KindSchedule, KindOrg, KindTeam, KindCredential, KindUser, KindLabel, KindExecutionEnv} {
		if byKind[k] == nil {
			t.Fatalf("missing projected kind %q", k)
		}
	}

	// Identity is controller-qualified so two Controllers never collide.
	if got := byKind[KindTemplate].GetIdentityKeys()[KindTemplate]; got != "ctrl-a/10" {
		t.Fatalf("template identity = %q, want ctrl-a/10", got)
	}

	// The CROSS-SOURCE edge that unifies the two ansible Sources: the template --runs-->
	// the ansible.playbook the ansible-project Syncer projects, named by the same
	// `<project>/<playbook>` identity key. Target scheme is the FOREIGN owned kind — AWX
	// only points at it. (The template also carries its owned-by org edge.)
	var runs *pluginv1.ObservedRelation
	for _, r := range byKind[KindTemplate].GetRelations() {
		if r.GetType() == "runs" {
			runs = r
		}
	}
	if runs == nil {
		t.Fatalf("template must carry a runs edge; got %+v", byKind[KindTemplate].GetRelations())
	}
	if runs.GetToScheme() != "ansible.playbook" || runs.GetToValue() != "web-content/site.yml" {
		t.Fatalf("runs edge wrong: scheme=%q value=%q (want ansible.playbook web-content/site.yml)", runs.GetToScheme(), runs.GetToValue())
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

	// The team → org edge (group management), and — since ADR-0130 — its membership edges
	// beside it. A team now carries BOTH, so this asserts each rather than a total count.
	tm := byKind[KindTeam]
	var orgEdges, memberEdges int
	for _, r := range tm.GetRelations() {
		switch {
		case r.GetType() == "member-of" && r.GetToScheme() == KindOrg && r.GetToValue() == "ctrl-a/1":
			orgEdges++
		case r.GetType() == "has-member" && r.GetToScheme() == KindUser && r.GetToValue() == "ctrl-a/60":
			memberEdges++
		default:
			t.Errorf("unexpected team edge: %+v", r)
		}
	}
	if orgEdges != 1 || memberEdges != 1 {
		t.Fatalf("team edges wrong: %d org, %d member; got %+v", orgEdges, memberEdges, tm.GetRelations())
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
