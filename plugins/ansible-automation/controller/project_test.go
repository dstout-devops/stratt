package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── ADR-0154 D3 · the credential that hides in a clone URL ───────────────────────────────

// The table is the decision. Each row is a shape a real AWX estate contains, and the
// `redacted` column is what a reader is being told — which has to be TRUE, because the flag
// is the only thing distinguishing a clean URL from a scrubbed one.
func TestRedactSCMURL(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     string
		redacted bool
	}{
		{
			// The case this exists for. Embedding a PAT in the clone URL works, so estates do it.
			name: "an embedded PAT is removed and the repository is kept",
			in:   "https://svc-account:ghp_REALTOKEN@github.example.com/acme/vendor.git",
			want: "https://github.example.com/acme/vendor.git", redacted: true,
		}, {
			name: "an ordinary https URL passes through untouched",
			in:   "https://github.com/example/infra.git",
			want: "https://github.com/example/infra.git", redacted: false,
		}, {
			// A BARE username is not a secret, and flagging it would make the flag mean
			// "there was an @" rather than "a credential was present" — which is the
			// difference between a signal and noise.
			name: "a bare username is not a credential",
			in:   "https://git@github.com/example/infra.git",
			want: "https://git@github.com/example/infra.git", redacted: false,
		}, {
			// scp-style. `git@host:path` is the benign form and `user:token@host` the
			// dangerous one, and they are not reliably distinguishable here — so the value is
			// withheld rather than guessed at (§2.5).
			name: "an unparseable value containing @ is withheld entirely",
			in:   "git@github.com:acme/infra.git",
			want: "", redacted: true,
		}, {
			name: "a local path has nowhere to hide a credential",
			in:   "/var/lib/awx/projects/local", want: "/var/lib/awx/projects/local", redacted: false,
		}, {
			name: "a manual project has no url at all",
			in:   "", want: "", redacted: false,
		}, {
			name: "an empty password is still a password field",
			in:   "https://svc:@github.com/acme/infra.git",
			want: "https://github.com/acme/infra.git", redacted: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, redacted := redactSCMURL(c.in)
			if got != c.want || redacted != c.redacted {
				t.Fatalf("redactSCMURL(%q) = (%q, %v), want (%q, %v)", c.in, got, redacted, c.want, c.redacted)
			}
			if strings.Contains(got, "REALTOKEN") {
				t.Fatal("a live token reached the projected value")
			}
		})
	}
}

// End to end against the simulator's seeded estate, which carries a PAT-bearing URL on
// purpose: a fixture without one would let a verbatim projection pass every test above and
// still leak in production.
func TestProjectProjectionLeaksNoEmbeddedCredential(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	ents, err := c.Normalize(&Snapshot{Projects: []Project{
		{ID: 3, Name: "vendor", ScmType: "git",
			ScmURL:      "https://svc-account:ghp_REALTOKENHERE@github.example.com/acme/vendor.git",
			ScmRevision: "abc0123", Status: "failed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	blob := searchable(t, ents)
	for _, secret := range []string{"ghp_REALTOKENHERE", "svc-account:"} {
		if strings.Contains(blob, secret) {
			t.Errorf("the projection carries %q: %s", secret, blob)
		}
	}
	// …and the fact an operator needs — WHICH repository — survives.
	if !strings.Contains(blob, "github.example.com/acme/vendor.git") {
		t.Errorf("the repository was lost along with the credential: %s", blob)
	}

	var facet map[string]any
	if err := json.Unmarshal(ents[0].GetFacets()[KindProject], &facet); err != nil {
		t.Fatal(err)
	}
	if facet["scmUrlRedacted"] != true {
		t.Error("a scrubbed URL must be distinguishable from a clean one — that is the whole " +
			"reason the flag exists, and 'cloned with an embedded credential' is its own finding")
	}
	// The fact that binds catalogue to execution.
	if facet["scmRevision"] != "abc0123" {
		t.Errorf("scmRevision is the only thing in the mirror saying which BYTES the Controller "+
			"runs: %v", facet)
	}
}

// ── D1 · the edge that makes the orphan signal diagnosable ───────────────────────────────

// Two edges, joined two different ways, and that is the point. `runs` joins on the project
// NAME an operator aligned to an env var; `uses-project` joins on the ID AWX issued. When
// the first drops and the second does not, the missing half is the CONTENT root — which is
// a diagnosis, where before there was only a dropped edge.
func TestTemplateCarriesBothTheNameJoinAndTheIDJoin(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	var jt JobTemplate
	if err := json.Unmarshal([]byte(`{"id":10,"name":"Deploy","playbook":"site.yml",
		"summary_fields":{"project":{"id":1,"name":"infra"}}}`), &jt); err != nil {
		t.Fatal(err)
	}
	ents, err := c.Normalize(&Snapshot{JobTemplates: []JobTemplate{jt}})
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string]string{}
	for _, r := range ents[0].GetRelations() {
		byType[r.GetType()] = r.GetToScheme() + ":" + r.GetToValue()
	}
	// The NAME join, unchanged: the content half identifies playbooks by project id and
	// relative path and knows nothing of AWX ids, so translating would have Stratt assert a
	// correspondence neither system states (§1.2).
	if byType["runs"] != "ansible.playbook:infra/site.yml" {
		t.Errorf("the runs edge must keep its name join: %v", byType)
	}
	// The ID join, new. It cannot silently mismatch.
	if byType["uses-project"] != "ansible.project:ctrl-a/1" {
		t.Errorf("uses-project must join on the id AWX issued: %v", byType)
	}
}

// A template with no project (AWX returns none, or a manual one that summary_fields omits)
// draws NO edge rather than one pointing at id 0 — which would resolve to nothing and read
// as a project that exists.
func TestNoProjectDrawsNoEdge(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	ents, err := c.Normalize(&Snapshot{JobTemplates: []JobTemplate{{ID: 10, Name: "Deploy"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ents[0].GetRelations() {
		if r.GetType() == "uses-project" {
			t.Fatalf("a template with no project must draw no edge, got %v", r.GetToValue())
		}
	}
}

// A MANUAL project (scm_type empty) is a directory on the Controller, which by definition no
// content root mirrors. It must still project — "this template runs content that lives only
// on the AWX box" is precisely the migration fact worth having.
func TestManualProjectStillProjects(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	ents, err := c.Normalize(&Snapshot{Projects: []Project{
		{ID: 2, Name: "local-scripts", Status: "never updated"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var facet map[string]any
	if err := json.Unmarshal(ents[0].GetFacets()[KindProject], &facet); err != nil {
		t.Fatal(err)
	}
	if facet["name"] != "local-scripts" || facet["scmType"] != "" || facet["scmUrlRedacted"] != false {
		t.Errorf("a manual project projects as itself: %v", facet)
	}
}
