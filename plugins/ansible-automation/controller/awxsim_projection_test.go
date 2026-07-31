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
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
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
		{"schedules", len(snap.Schedules), 4}, // > pageSize: the `next` cursor is followed
		{"organizations", len(snap.Organizations), 2},
		{"teams", len(snap.Teams), 2},
		{"credentials", len(snap.Credentials), 2},
		{"users", len(snap.Users), 4},
		{"labels", len(snap.Labels), 3},
		{"execution environments", len(snap.ExecutionEnvs), 2},
		{"notification templates", len(snap.Notifications), 3},
	} {
		if tc.got != tc.want {
			t.Errorf("%s enumerated = %d, want %d", tc.what, tc.got, tc.want)
		}
	}

	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(ents) != 25 {
		t.Fatalf("projected %d entities, want 25 (2 templates + 1 workflow + 4 schedules + 2 orgs + 2 teams + "+
			"2 credentials + 4 users + 3 labels + 2 execution environments + 3 notification templates)", len(ents))
	}

	byKind := map[string]int{}
	for _, e := range ents {
		byKind[e.GetKind()]++
	}
	for kind, want := range map[string]int{
		KindTemplate: 2, KindWorkflow: 1, KindSchedule: 4, KindOrg: 2, KindTeam: 2, KindCredential: 2, KindUser: 4, KindLabel: 3, KindExecutionEnv: 2,
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
	// ADR-0128 D2: the edge that makes "which templates use this credential" a traversal.
	// Same-source (both ends owned by this half), so it always resolves.
	if !has("ctrl-a/10", edge{"uses-credential", KindCredential, "ctrl-a/1"}) {
		t.Errorf("template 10 has no uses-credential edge onto prod-ssh; got %v", edges["ctrl-a/10"])
	}
}

// ADR-0129: the workflow says what it invokes. awxsim's prod-pipeline has five nodes —
// build(jt11), approve(workflow_approval), deploy(jt10), notify-ok(jt11), rollback(jt11) —
// so it must yield exactly TWO invokes edges (distinct targets, not five), and record the
// approval gate as a fact rather than an edge.
func TestWorkflowTopologyProjects(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if got := len(snap.WorkflowNodes[20]); got != 5 {
		t.Fatalf("workflow 20 node count = %d, want 5 (the N+1 per-workflow read)", got)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for _, e := range ents {
		if e.GetIdentityKeys()[KindWorkflow] != "ctrl-a/20" {
			continue
		}
		var invokes []string
		for _, r := range e.GetRelations() {
			if r.GetType() != "invokes" {
				continue
			}
			if r.GetToScheme() != KindTemplate {
				t.Errorf("invokes edge targets %q, want %q for a `job` node", r.GetToScheme(), KindTemplate)
			}
			invokes = append(invokes, r.GetToValue())
		}
		sort.Strings(invokes)
		want := []string{"ctrl-a/10", "ctrl-a/11"}
		if !reflect.DeepEqual(invokes, want) {
			t.Errorf("invokes = %v, want %v — one edge per DISTINCT target (three nodes run jt11)", invokes, want)
		}

		var facet map[string]any
		if err := json.Unmarshal(e.GetFacets()[KindWorkflow], &facet); err != nil {
			t.Fatalf("workflow facet: %v", err)
		}
		if facet["hasApprovalGate"] != true {
			t.Errorf("hasApprovalGate = %v, want true — an approval node is a pause with no target, so it is a fact not an edge", facet["hasApprovalGate"])
		}
		if facet["nodeCount"] != float64(5) {
			t.Errorf("nodeCount = %v, want 5", facet["nodeCount"])
		}
		return
	}
	t.Fatal("workflow ctrl-a/20 was not projected")
}

// The §1.8 call in ADR-0129 D1: a node type we do not project draws NO edge rather than a
// confidently wrong one. A workflow node can legitimately target a project sync or an
// inventory update, and guessing would point at an identity that exists and means
// something else.
func TestUnknownWorkflowNodeTypeDrawsNoEdge(t *testing.T) {
	c := New(Config{Endpoint: "https://aap.example.com", ControllerID: "ctrl-a"})
	mk := func(ujt int, typ string) WorkflowNode {
		var n WorkflowNode
		n.UnifiedJobTemplate = ujt
		n.SummaryFields.UnifiedJobTemplate.ID = ujt
		n.SummaryFields.UnifiedJobTemplate.UnifiedJobType = typ
		return n
	}
	rels, gate := c.workflowTopology([]WorkflowNode{
		mk(7, "project_update"),
		mk(8, "inventory_update"),
		mk(9, "system_job"),
		mk(10, "some_future_awx_type"),
	})
	if len(rels) != 0 {
		t.Errorf("unprojected node types drew %d edges, want 0 — a wrong edge is worse than a missing one (§1.8)", len(rels))
	}
	if gate {
		t.Error("no approval node present, yet hasApprovalGate is true")
	}
	// And the nested-workflow case does resolve, onto the workflow scheme.
	rels, _ = c.workflowTopology([]WorkflowNode{mk(21, "workflow_job")})
	if len(rels) != 1 || rels[0].GetToScheme() != KindWorkflow || rels[0].GetToValue() != "ctrl-a/21" {
		t.Errorf("nested workflow node = %+v, want one invokes edge onto ansible.workflow ctrl-a/21", rels)
	}
}

// ADR-0130 D2: team membership is an edge, and it is an ESTATE fact. awxsim's web-ops has
// two members, dba has one. Same-source, so it always resolves.
func TestTeamMembershipProjects(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	members := map[string][]string{}
	for _, e := range ents {
		id := e.GetIdentityKeys()[KindTeam]
		if e.GetKind() != KindTeam {
			continue
		}
		for _, r := range e.GetRelations() {
			if r.GetType() != "has-member" {
				continue
			}
			if r.GetToScheme() != KindUser {
				t.Errorf("has-member targets %q, want %q", r.GetToScheme(), KindUser)
			}
			members[id] = append(members[id], r.GetToValue())
		}
	}
	for team, want := range map[string][]string{
		"ctrl-a/1": {"ctrl-a/60", "ctrl-a/61"},
		"ctrl-a/2": {"ctrl-a/62"},
	} {
		got := append([]string{}, members[team]...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("team %s members = %v, want %v", team, got, want)
		}
	}
}

// The field awx-superuser-review reads, and the §2.5 line beside it: an account mirror
// carries no password hash, no token, no material of any kind.
func TestUserProjectionIsAccountFactsOnly(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	allowed := map[string]bool{"username": true, "email": true, "isActive": true, "isSuperuser": true, "isSystemAuditor": true}
	var supers, seen int
	for _, e := range ents {
		if e.GetKind() != KindUser {
			continue
		}
		seen++
		var facet map[string]any
		if err := json.Unmarshal(e.GetFacets()[KindUser], &facet); err != nil {
			t.Fatalf("user facet: %v", err)
		}
		for k := range facet {
			if !allowed[k] {
				t.Errorf("user facet carries %q — account facts only; no material, ever (§2.5)", k)
			}
		}
		if facet["isSuperuser"] == true {
			supers++
		}
		// It must NOT claim the identity plane: identity.subject has a single write-owner
		// (§2.1 / ADR-0079 slice-3 gate) and this is a different system of record.
		if _, claimed := e.GetFacets()["identity.subject"]; claimed {
			t.Error("the AWX account mirror claims identity.subject — that namespace is solely the SCIM projector's (§2.1); an AWX local account is not an identity")
		}
		if _, claimed := e.GetLabels()["identity.name"]; claimed {
			t.Error("the AWX account mirror claims the identity.name label key — solely the SCIM projector's (ADR-0041/§2.1)")
		}
	}
	if seen != 4 {
		t.Fatalf("projected %d users, want 4", seen)
	}
	if supers != 1 {
		t.Errorf("superusers = %d, want 1 — the account awx-superuser-review exists to surface", supers)
	}
}

// ADR-0132 D1: an AWX label is an ENTITY and membership is an edge, so "every production
// job template" is a View over topology rather than a scan. awxsim puts `prod` and
// `critical` on jt10 and `legacy` on jt11.
func TestLabelsProjectAsEntitiesWithEdges(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	byLabelEdge := map[string][]string{}
	var labelEnts int
	for _, e := range ents {
		if e.GetKind() == KindLabel {
			labelEnts++
			continue
		}
		id := e.GetIdentityKeys()[e.GetKind()]
		for _, r := range e.GetRelations() {
			if r.GetType() != "has-label" {
				continue
			}
			if r.GetToScheme() != KindLabel {
				t.Errorf("has-label targets %q, want %q", r.GetToScheme(), KindLabel)
			}
			byLabelEdge[id] = append(byLabelEdge[id], r.GetToValue())
		}
	}
	if labelEnts != 3 {
		t.Errorf("projected %d label entities, want 3", labelEnts)
	}
	for tmpl, want := range map[string][]string{
		"ctrl-a/10": {"ctrl-a/70", "ctrl-a/71"},
		"ctrl-a/11": {"ctrl-a/72"},
	} {
		got := append([]string{}, byLabelEdge[tmpl]...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("template %s has-label = %v, want %v", tmpl, got, want)
		}
	}
	// The label is an entity carrying its own facet, which is what makes it selectable as
	// a View target (ViewSelector.Relations targetLabels).
	for _, e := range ents {
		if e.GetKind() == KindLabel && e.GetIdentityKeys()[KindLabel] == "ctrl-a/70" {
			if got := e.GetLabels()["ansible.name"]; got != "prod" {
				t.Errorf("label entity ansible.name = %q, want prod — the key a View selects the target by", got)
			}
			return
		}
	}
	t.Fatal("label ctrl-a/70 was not projected")
}

// ADR-0132 D3: two schedules of ONE template, distinguishable — and the §2.5 line held.
// awxsim's nightly-deploy and canary-deploy both launch jt10 and differ only in their
// extra_data and limit, which is precisely the case AWX-013 said the mirror could not
// tell apart.
func TestScheduleShapeDistinguishesAndCarriesNoValues(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	facets := map[string]map[string]any{}
	for _, e := range ents {
		if e.GetKind() != KindSchedule {
			continue
		}
		var f map[string]any
		if err := json.Unmarshal(e.GetFacets()[KindSchedule], &f); err != nil {
			t.Fatalf("schedule facet: %v", err)
		}
		facets[e.GetIdentityKeys()[KindSchedule]] = f
	}
	nightly, canary := facets["ctrl-a/30"], facets["ctrl-a/33"]
	if nightly == nil || canary == nil {
		t.Fatalf("both schedules of jt10 must project; got %v", len(facets))
	}
	if reflect.DeepEqual(nightly, canary) {
		t.Fatal("two schedules of one template are still indistinguishable — the whole of AWX-013")
	}
	if got := nightly["timezone"]; got != "Europe/London" {
		t.Errorf("timezone = %v — an rrule with no timezone is under-determined", got)
	}
	if got := nightly["limit"]; got != "web*" {
		t.Errorf("per-schedule limit override = %v, want web*", got)
	}

	// KEY NAMES only. A value reaching the graph here would walk around ADR-0128 D4
	// through a side door.
	wantKeys := []any{"app_version", "canary"}
	if !reflect.DeepEqual(canary["extraDataKeys"], wantKeys) {
		t.Errorf("extraDataKeys = %v, want %v (sorted key names)", canary["extraDataKeys"], wantKeys)
	}
	blob, _ := json.Marshal(canary)
	for _, secret := range []string{"2.0-rc1", "gold", "1.0"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("a schedule extra_data VALUE (%q) reached the graph — §2.5 / ADR-0132 D3 project key names only", secret)
		}
	}
}

// ADR-0133 D1/D2: the EE projects as a supply-chain fact, the runs-in edge makes image
// blast radius a traversal, and digestPinned is derived from the reference. awxsim seeds
// one pinned EE and one on a floating tag — the Baseline's two cases in one estate.
func TestExecutionEnvironmentProjectsWithDerivedPinning(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	pinned := map[string]bool{}
	runsIn := map[string]string{}
	for _, e := range ents {
		switch e.GetKind() {
		case KindExecutionEnv:
			var f map[string]any
			if err := json.Unmarshal(e.GetFacets()[KindExecutionEnv], &f); err != nil {
				t.Fatalf("ee facet: %v", err)
			}
			name, _ := f["name"].(string)
			ok, _ := f["digestPinned"].(bool)
			pinned[name] = ok
			if f["image"] == "" {
				t.Errorf("EE %q projected digestPinned without the image — a Finding that cannot say WHAT is unpinned fails §1.8", name)
			}
		case KindTemplate:
			for _, r := range e.GetRelations() {
				if r.GetType() == "runs-in" {
					if r.GetToScheme() != KindExecutionEnv {
						t.Errorf("runs-in targets %q, want %q", r.GetToScheme(), KindExecutionEnv)
					}
					runsIn[e.GetIdentityKeys()[KindTemplate]] = r.GetToValue()
				}
			}
		}
	}
	if got, ok := pinned["pinned-ee"]; !ok || !got {
		t.Errorf("digest-referenced EE projected digestPinned=%v, want true", got)
	}
	if got, ok := pinned["floating-ee"]; !ok || got {
		t.Errorf("tag-referenced EE projected digestPinned=%v, want false — this is the case §7.3 exists for", got)
	}
	if runsIn["ctrl-a/10"] != "ctrl-a/80" || runsIn["ctrl-a/11"] != "ctrl-a/81" {
		t.Errorf("runs-in edges wrong: %v", runsIn)
	}
}

// digestPinned is a parse, and the cases that matter are the ones that could be read
// wrong: a registry path containing "@", and a reference carrying BOTH a tag and a digest.
func TestDigestPinnedParse(t *testing.T) {
	for image, want := range map[string]bool{
		"quay.io/ansible/awx-ee@sha256:" + strings.Repeat("a", 64):     true,
		"quay.io/ansible/awx-ee:1.2@sha256:" + strings.Repeat("b", 64): true, // digest wins over the tag
		"quay.io/ansible/awx-ee:latest":                                false,
		"quay.io/ansible/awx-ee":                                       false,
		"registry@corp.io/ansible/awx-ee:1.0":                          false, // "@" in the REGISTRY, not a digest
		"quay.io/ansible/awx-ee@sha256:":                               false, // empty digest
		"quay.io/ansible/awx-ee@md5:abc":                               false,
		"":                                                             false,
	} {
		if got := digestPinned(image); got != want {
			t.Errorf("digestPinned(%q) = %v, want %v", image, got, want)
		}
	}
}

// §2.5 as a test, not a promise: the credential mirror carries NAME AND KIND and nothing
// else. The closed schema refuses anything more at the write path; this refuses it at the
// source, so a future field cannot arrive here and be silently dropped downstream.
func TestCredentialProjectionCarriesNoMaterial(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var seen int
	for _, e := range ents {
		if e.GetKind() != KindCredential {
			continue
		}
		seen++
		var facet map[string]any
		if err := json.Unmarshal(e.GetFacets()[KindCredential], &facet); err != nil {
			t.Fatalf("credential facet: %v", err)
		}
		for k := range facet {
			if k != "name" && k != "kind" {
				t.Errorf("credential facet carries %q — name and kind ONLY (§2.5); material must never be projected", k)
			}
		}
		if facet["name"] == "" {
			t.Error("credential projected with no name")
		}
	}
	if seen != 2 {
		t.Fatalf("projected %d credentials, want 2", seen)
	}
}

// Run state reaches the graph (ADR-0128 D1/D3) — the fields the awx-template-failing
// Baseline reads. awxsim's Deploy Web is seeded failed.
func TestRunStateProjects(t *testing.T) {
	c := simClient(t)
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	for _, e := range ents {
		if e.GetIdentityKeys()[KindTemplate] != "ctrl-a/10" {
			continue
		}
		var facet map[string]any
		if err := json.Unmarshal(e.GetFacets()[KindTemplate], &facet); err != nil {
			t.Fatalf("template facet: %v", err)
		}
		if facet["lastRunFailed"] != true {
			t.Errorf("lastRunFailed = %v, want true — the field the failing-template Baseline reads", facet["lastRunFailed"])
		}
		if facet["lastRunStatus"] != "failed" {
			t.Errorf("lastRunStatus = %v, want failed", facet["lastRunStatus"])
		}
		if facet["limit"] != "web*" {
			t.Errorf("limit = %v, want web* — a run knob the mirror was blind to", facet["limit"])
		}
		if _, ok := facet["extraVars"]; ok {
			t.Error("extraVars is projected — it may carry secret material and must never reach the graph (§2.5, ADR-0128 D4)")
		}
		return
	}
	t.Fatal("template ctrl-a/10 was not projected")
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
