package materialize

import (
	"encoding/json"
	"strings"
	"testing"

	awx "github.com/dstout-devops/stratt/plugins/ansible-automation/controller/awxapi"
)

func TestReduceHostFilter(t *testing.T) {
	labels, facets, irr := reduceHostFilter("groups__name=prod and ansible_facts__ansible_distribution__family=RedHat and name__icontains=web")
	if labels["awx.group.name"] != "prod" {
		t.Errorf("group label: %v", labels)
	}
	if len(facets) != 1 || facets[0].Namespace != "ansible_distribution" || facets[0].Path != "family" || facets[0].Equals != "RedHat" {
		t.Errorf("facet: %+v", facets)
	}
	if len(irr) != 1 || !strings.Contains(irr[0], "name__icontains=web") {
		t.Errorf("irreducible: %v", irr)
	}

	// or/not/parens → the whole filter is irreducible, no partial selector.
	l2, f2, irr2 := reduceHostFilter("name=a or name=b")
	if len(l2) != 0 || len(f2) != 0 || len(irr2) != 1 {
		t.Errorf("disjunction must be fully irreducible: %v %v %v", l2, f2, irr2)
	}
}

func TestStaticInventoryNeverProjectsHosts(t *testing.T) {
	snap := &awx.Snapshot{
		Inventories:      []awx.Inventory{{ID: 1, Name: "legacy", Kind: ""}},
		Hosts:            map[int][]awx.Host{1: {{Name: "h1"}, {Name: "h2"}}},
		InventorySources: map[int][]awx.InventorySource{},
	}
	r := newReport()
	name, doc, err := mapInventory(snap, snap.Inventories[0], r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "awx.inventory.name: legacy") {
		t.Errorf("static View should select on the compat label:\n%s", doc)
	}
	// The hosts must never appear as projected Entities/targets in the bundle.
	if strings.Contains(doc, "h1") || strings.Contains(doc, "h2") {
		t.Errorf("manual hosts must not be projected into the View:\n%s", doc)
	}
	if len(r.blocking) == 0 {
		t.Errorf("static inventory must raise a blocking item for %q", name)
	}
}

func TestDynamicInventoryPointsAtNativeSyncer(t *testing.T) {
	snap := &awx.Snapshot{
		Inventories:      []awx.Inventory{{ID: 2, Name: "cloud", Kind: ""}},
		InventorySources: map[int][]awx.InventorySource{2: {{Source: "aws_ec2"}}},
		Hosts:            map[int][]awx.Host{},
	}
	r := newReport()
	_, doc, err := mapInventory(snap, snap.Inventories[0], r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "kinds:") || !strings.Contains(doc, "instance") {
		t.Errorf("aws_ec2 View should select kind instance:\n%s", doc)
	}
	if !hasNote(r, "awsec2") {
		t.Errorf("dynamic inventory should recommend the native awsec2 Connector; notes=%v", r.notes)
	}
}

func TestJobTemplateScmContentRef(t *testing.T) {
	snap := &awx.Snapshot{
		JobTemplates: []awx.JobTemplate{{ID: 10, Name: "Deploy", Playbook: "site.yml", Project: 1, Inventory: 2}},
		Projects:     map[int]awx.Project{1: {ID: 1, ScmType: "git", ScmURL: "https://x/repo.git", ScmBranch: "main"}},
	}
	r := newReport()
	doc, err := mapJobTemplate(snap, snap.JobTemplates[0], map[int]string{2: "awx/cloud"}, map[int]string{}, "awx/deploy", r, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"actuator: ansible", "viewName: awx/cloud", "repo: https://x/repo.git", "playbook: site.yml", "ref: main"} {
		if !strings.Contains(doc, want) {
			t.Errorf("job-template Step missing %q:\n%s", want, doc)
		}
	}
}

func TestJobTemplateManualProjectGetsPlaceholderPlay(t *testing.T) {
	snap := &awx.Snapshot{
		JobTemplates: []awx.JobTemplate{{ID: 11, Name: "Local", Playbook: "facts.yml", Project: 2, Inventory: 1}},
		Projects:     map[int]awx.Project{2: {ID: 2, ScmType: "", ScmURL: ""}},
	}
	r := newReport()
	doc, err := mapJobTemplate(snap, snap.JobTemplates[0], map[int]string{1: "awx/legacy"}, map[int]string{}, "awx/local", r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "play:") || !strings.Contains(doc, "MIGRATION TODO") {
		t.Errorf("manual project should get a placeholder play:\n%s", doc)
	}
	if strings.Contains(doc, "scm:") {
		t.Errorf("manual project must not emit an (invalid) empty scm:\n%s", doc)
	}
	if len(r.blocking) == 0 {
		t.Error("manual project must raise a blocking item")
	}
}

func TestWorkflowEdgesApprovalAndFanout(t *testing.T) {
	snap := &awx.Snapshot{
		JobTemplates: []awx.JobTemplate{
			{ID: 10, Name: "build", Playbook: "b.yml", Project: 1, Inventory: 1},
			{ID: 11, Name: "deploy", Playbook: "d.yml", Project: 1, Inventory: 1},
		},
		Projects:    map[int]awx.Project{1: {ScmType: "git", ScmURL: "https://x/r.git"}},
		WorkflowJTs: []awx.WorkflowJobTemplate{{ID: 20, Name: "pipe"}},
		WorkflowNodes: map[int][]awx.WorkflowNode{20: {
			mkNode(100, "build", 10, "job", []int{101}, nil),
			mkApproval(101, "approve", []int{102}),
			mkNode(102, "deploy", 11, "job", []int{103}, []int{104}),
			mkNode(103, "ok", 10, "job", nil, nil),
			mkNode(104, "bad", 10, "job", nil, nil),
		}},
	}
	r := newReport()
	doc, err := mapWorkflow(snap, snap.WorkflowJTs[0], map[int]string{1: "awx/inv"}, map[int]string{}, "awx/pipe", r, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: approve", "gate:", "when: success", "when: failure", "needs:"} {
		if !strings.Contains(doc, want) {
			t.Errorf("workflow missing %q:\n%s", want, doc)
		}
	}
	if !hasBlock(r, "approver identity") {
		t.Errorf("approval node must block on approver identity; blocking=%v", r.blocking)
	}
}

func TestSurveyToInputContract(t *testing.T) {
	spec := awx.SurveySpec{Name: "s", Spec: []awx.SurveyQuestion{
		{Variable: "ver", Type: "text", Required: true, Default: json.RawMessage(`"1.0"`)},
		{Variable: "n", Type: "integer", Min: iptr(1), Max: iptr(9)},
		{Variable: "tier", Type: "multiplechoice", Choices: json.RawMessage(`["gold","silver"]`)},
		{Variable: "tok", Type: "password"},
	}}
	r := newReport()
	schema, vars, err := mapSurvey(awx.JobTemplate{Name: "Deploy"}, spec, "awx/deploy", r)
	if err != nil {
		t.Fatal(err)
	}
	// The schema must satisfy what a Workflow's declared launch interface requires
	// (ADR-0118 D2), because that is now where it lands — an open or non-object survey
	// would fail the importer's own bundle parse.
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("a survey must render as a CLOSED object schema, or CompileInputSchema rejects the "+
			"Workflow it lands on: type=%v additionalProperties=%v", schema["type"], schema["additionalProperties"])
	}
	// No $id: these are git-declared estate inputs, not registry Contracts. An $id would
	// advertise a rung-2 pin and hash-verification that never happens (§1.8).
	if _, ok := schema["$id"]; ok {
		t.Errorf("a Workflow's inputs carry no registry identity; $id would claim a pin that does not exist: %v", schema["$id"])
	}
	if strings.Join(vars, ",") != "n,tier,ver" {
		t.Errorf("answerable variables must come back sorted, password excluded, got %v", vars)
	}
	props := schema["properties"].(map[string]any)
	if props["n"].(map[string]any)["type"] != "integer" {
		t.Errorf("integer question type: %v", props["n"])
	}
	// A password question is secret material. Importing it as a launch input would carry
	// that material onto the Run, into the audit stream, and out to --extra-vars, whose
	// own Contract forbids it (§2.5) — so it must be refused and re-brokered, not typed.
	// Marking it x-stratt-sensitive was adequate while the document was detached and
	// enforced by nothing; it is not adequate now that inputs are live.
	if _, ok := props["tok"]; ok {
		t.Errorf("a password question must NOT become a launch input (§2.5): %v", props["tok"])
	}
	if !hasBlock(r, "must be brokered") {
		t.Errorf("a password question must BLOCK the bundle on re-broker, not pass with a note; blocking=%v", r.blocking)
	}
	if _, ok := props["tier"].(map[string]any)["enum"]; !ok {
		t.Errorf("multiplechoice must produce enum: %v", props["tier"])
	}
	req, _ := schema["required"].([]string)
	if len(req) != 1 || req[0] != "ver" {
		t.Errorf("required: %v", schema["required"])
	}
}

func TestCredentialImportsNoMaterial(t *testing.T) {
	r := newReport()
	_, doc, err := mapCredential(awx.Credential{ID: 1, Name: "prod-ssh", Kind: "ssh"}, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ownerTeam: REVIEW-ME", "backend: k8s-secret", "id_ssh"} {
		if !strings.Contains(doc, want) {
			t.Errorf("credential ref missing %q:\n%s", want, doc)
		}
	}
	// No material tokens whatsoever.
	for _, bad := range []string{"$encrypted$", "private_key:", "BEGIN "} {
		if strings.Contains(doc, bad) {
			t.Errorf("credential ref must carry no material (%q):\n%s", bad, doc)
		}
	}
	if len(r.blocking) == 0 {
		t.Error("credential must block on re-broker")
	}
}

// helpers

func mkNode(id int, ident string, ujt int, ujtType string, ok, fail []int) awx.WorkflowNode {
	n := awx.WorkflowNode{ID: id, Identifier: ident, UnifiedJobTemplate: ujt, SuccessNodes: ok, FailureNodes: fail}
	n.SummaryFields.UnifiedJobTemplate.ID = ujt
	n.SummaryFields.UnifiedJobTemplate.UnifiedJobType = ujtType
	return n
}

func mkApproval(id int, ident string, ok []int) awx.WorkflowNode {
	n := awx.WorkflowNode{ID: id, Identifier: ident, SuccessNodes: ok, Timeout: 3600}
	n.SummaryFields.UnifiedJobTemplate.UnifiedJobType = "workflow_approval"
	return n
}

func iptr(n int) *int { return &n }

func hasNote(r *report, sub string) bool  { return anyContains(r.notes, sub) }
func hasBlock(r *report, sub string) bool { return anyContains(r.blocking, sub) }
func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestJobTemplateCheckMapsToDryRun locks ADR-0117 D2's redirect: an imported AWX
// "check" job template must land on the Step's DryRun bit — the ONE check-mode
// mechanism (ADR-0051 MF6) — and must NOT emit params.check, which the runtime
// reads nowhere. Getting this wrong silently converts an imported check job into a
// converging apply on its first run (§1.8), which is why it is pinned here.
func TestJobTemplateCheckMapsToDryRun(t *testing.T) {
	snap := &awx.Snapshot{
		JobTemplates: []awx.JobTemplate{{ID: 11, Name: "Audit", Playbook: "audit.yml", Project: 1, Inventory: 2, JobType: "check"}},
		Projects:     map[int]awx.Project{1: {ID: 1, ScmType: "git", ScmURL: "https://x/repo.git"}},
	}
	r := newReport()
	doc, err := mapJobTemplate(snap, snap.JobTemplates[0], map[int]string{2: "awx/cloud"}, map[int]string{}, "awx/audit", r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "dryRun: true") {
		t.Errorf("check job template must set the Step dryRun bit:\n%s", doc)
	}
	if strings.Contains(doc, "check: true") {
		t.Errorf("check job template must NOT emit the deprecated params.check:\n%s", doc)
	}

	// A normal (run) template stays an applying Step.
	snap.JobTemplates[0].JobType = "run"
	doc, err = mapJobTemplate(snap, snap.JobTemplates[0], map[int]string{2: "awx/cloud"}, map[int]string{}, "awx/audit", r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "dryRun") {
		t.Errorf("a run template must not be marked dryRun:\n%s", doc)
	}
}
