package awxfacade

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/types"
)

func gated() types.Workflow {
	return types.Workflow{
		Name: "cert-issue",
		Steps: []types.Step{
			{Name: "issue", ViewName: "web-hosts", Actuator: "cert-issuer"},
			{Name: "approve", Needs: []string{"issue"}, Gate: &types.GateSpec{
				Approvers: types.GateApprovers{Teams: []string{"platform"}}, Threshold: 2,
			}},
			{Name: "deliver", Needs: []string{"approve"}, ViewName: "web-hosts", Actuator: "ansible"},
			{Name: "notify-failure", Needs: []string{"deliver"}, When: types.WhenFailure, Action: "slack/post"},
			{Name: "always-record", Needs: []string{"deliver"}, When: types.WhenAlways, Action: "audit/record"},
		},
	}
}

// THE PARTITION, checked against the Workflows the repo actually ships rather than a fixture: a
// Workflow that fell through the gap between the two families would be INVISIBLE on the compat
// surface, which is a harder failure to notice than a wrong rendering. Before this family existed
// every multi-Step Workflow in the estate was in exactly that hole.
func TestEveryWorkflowLandsInExactlyOneFamily(t *testing.T) {
	decls, err := desiredstate.ParseDir("../../../estate", nil)
	if err != nil {
		t.Skipf("reference estate not parseable here: %v", err)
	}
	if len(decls.Workflows) == 0 {
		t.Fatal("the reference estate declares no Workflows — this test would prove nothing")
	}
	wfjts := 0
	for _, wf := range decls.Workflows {
		_, jt := singleActuationStep(wf)
		if jt == isWFJT(wf) {
			t.Errorf("workflow %q is in %s families — the partition must be exact and total",
				wf.Name, map[bool]string{true: "BOTH", false: "NEITHER OF THE"}[jt])
		}
		if isWFJT(wf) {
			wfjts++
		}
	}
	if wfjts == 0 {
		t.Error("no shipped Workflow renders as a workflow_job_template — either the estate lost " +
			"every DAG or isWFJT is broken; both are worth failing on")
	}
	t.Logf("%d of %d shipped Workflows are workflow_job_templates", wfjts, len(decls.Workflows))
}

// Stratt's `needs` points BACKWARD at prerequisites; AWX's success/failure/always lists point
// FORWARD at successors. Inverting them wrong draws a client a DAG that runs in reverse, and it
// would look entirely plausible.
func TestWorkflowNodesInvertTheEdgeDirection(t *testing.T) {
	wf := gated()
	nodes := workflowNodes(wf)
	if len(nodes) != len(wf.Steps) {
		t.Fatalf("got %d nodes for %d steps — every Step is a node, including the ones AWX has no "+
			"concept for; omitting them would show a DAG that differs from the one that runs",
			len(nodes), len(wf.Steps))
	}
	by := map[string]map[string]any{}
	for _, n := range nodes {
		m := n.(map[string]any)
		by[m["identifier"].(string)] = m
	}

	ids := func(step, key string) []int64 { return by[step][key].([]int64) }
	want := func(step, key string, successors ...string) {
		t.Helper()
		got := ids(step, key)
		if len(got) != len(successors) {
			t.Fatalf("%s.%s = %v, want %v", step, key, got, successors)
		}
		for i, s := range successors {
			if got[i] != nodeID(wf.Name, s) {
				t.Errorf("%s.%s[%d] does not point at %q", step, key, i, s)
			}
		}
	}
	want("issue", "success_nodes", "approve")
	want("approve", "success_nodes", "deliver")
	want("deliver", "failure_nodes", "notify-failure")
	want("deliver", "always_nodes", "always-record")
	want("deliver", "success_nodes") // nothing succeeds `deliver` on success
	want("notify-failure", "success_nodes")
}

// An AWX field either carries its AWX meaning faithfully or is absent. `unified_job_template` names
// an object a client can FETCH — so a Step, which is not independently launchable, points at null
// rather than at a synthesized id that would 404 or, worse, collide with an unrelated Workflow.
func TestOnlyANestedWorkflowStepPointsAtAnotherTemplate(t *testing.T) {
	wf := types.Workflow{Name: "region-build", Steps: []types.Step{
		{Name: "subnet", ViewName: "built-subnets", Actuator: "opentofu"},
		{Name: "hosts", Needs: []string{"subnet"}, Workflow: "compute-build"},
		{Name: "check", Needs: []string{"hosts"}, Policy: &types.PolicySpec{Controls: make([]types.Control, 2)}},
	}}
	by := map[string]map[string]any{}
	for _, n := range workflowNodes(wf) {
		m := n.(map[string]any)
		by[m["identifier"].(string)] = m
	}
	if by["subnet"]["unified_job_template"] != nil {
		t.Errorf("an actuation Step is a node of THIS DAG with no template of its own; pointing "+
			"somewhere would be a dangling reference: %v", by["subnet"]["unified_job_template"])
	}
	if by["check"]["unified_job_template"] != nil {
		t.Error("a policy checkpoint has no AWX object to point at")
	}
	if by["hosts"]["unified_job_template"] != awxID("compute-build") {
		t.Errorf("a nested-Workflow Step names a real declared Workflow — that edge is true and is "+
			"the descent link into the child, got %v", by["hosts"]["unified_job_template"])
	}

	// The descent block: what each node actually IS, in Stratt's vocabulary, namespaced so no
	// client mistakes it for AWX.
	for step, shape := range map[string]string{"subnet": "actuation", "hosts": "nested-workflow", "check": "policy"} {
		sf := by[step]["summary_fields"].(map[string]any)["stratt"].(map[string]any)
		if sf["shape"] != shape {
			t.Errorf("%s: shape = %v, want %q", step, sf["shape"], shape)
		}
	}
}

func TestStepShapeNamesWhatAWXCannot(t *testing.T) {
	cases := []struct {
		step types.Step
		want string
	}{
		{types.Step{ViewName: "v", Actuator: "ansible"}, "actuation"},
		{types.Step{Gate: &types.GateSpec{}}, "gate"},
		{types.Step{Policy: &types.PolicySpec{}}, "policy"},
		{types.Step{Workflow: "child"}, "nested-workflow"},
		{types.Step{WorkflowCapability: "provisioning", ForKind: "Compute"}, "nested-workflow"},
		{types.Step{Action: "netbox/allocate"}, "action"},
		{types.Step{ActionCapability: "ipam"}, "action"},
	}
	for _, c := range cases {
		if got := stepShape(c.step); got != c.want {
			t.Errorf("stepShape = %q, want %q", got, c.want)
		}
	}
}

// `ask_variables_on_launch: true` on a Workflow that declares no interface advertises a door that
// contract.ResolveLaunchInputs then slams: the caller's extra_vars are REFUSED, naming the keys.
// Advertising it would make the façade the surface that invites a 400.
func TestSurveyIsAdvertisedOnlyWhenTheWorkflowDeclaresOne(t *testing.T) {
	bare := workflowToWFJT(gated())
	if bare["ask_variables_on_launch"] != false || bare["survey_enabled"] != false {
		t.Errorf("a Workflow with no `inputs` must not advertise a survey: %v", bare)
	}
	if _, present := bare["extra_vars"]; present {
		t.Error("no declared interface means no schema to publish")
	}

	wf := gated()
	wf.Inputs = json.RawMessage(`{"type":"object","additionalProperties":false,` +
		`"properties":{"commonName":{"type":"string"}}}`)
	surveyed := workflowToWFJT(wf)
	if surveyed["ask_variables_on_launch"] != true || surveyed["survey_enabled"] != true {
		t.Errorf("a declared `inputs` interface IS the survey on this surface (ADR-0118 D2): %v", surveyed)
	}
	raw, ok := surveyed["extra_vars"].(json.RawMessage)
	if !ok || len(raw) == 0 {
		t.Fatalf("the schema must be published so a client renders the same form the native door does: %v", surveyed["extra_vars"])
	}
}

// A gated Workflow is the shape job_templates skips, and the whole reason this family exists. Its
// WFJT must carry the two related links a client needs to do anything with it.
func TestWFJTCarriesLaunchAndNodesLinks(t *testing.T) {
	got := workflowToWFJT(gated())
	rel := got["related"].(map[string]any)
	id := awxID("cert-issue")
	if rel["launch"] != jt("/api/v2/workflow_job_templates/%d/launch/", id) {
		t.Errorf("launch link = %v", rel["launch"])
	}
	if rel["workflow_nodes"] != jt("/api/v2/workflow_job_templates/%d/workflow_nodes/", id) {
		t.Errorf("workflow_nodes link = %v", rel["workflow_nodes"])
	}
	if got["inventory"] != nil {
		t.Error("a DAG has no single View — each actuation Step names its own, so naming one here " +
			"would pick one for the caller")
	}
}

// The execution's AWX id and the native WorkflowRun id must BOTH be present: the first is what an
// AWX client polls, the second is what an operator descends with (§1.8).
func TestWorkflowJobCarriesBothIdentities(t *testing.T) {
	wr := types.WorkflowRun{ID: "550e8400-e29b-41d4-a716-446655440000", WorkflowName: "cert-issue",
		Status: types.RunRunning, TriggeredBy: "cert-reconcile"}
	got := workflowRunToJob(wr)
	if got["id"] != awxID(wr.ID) || got["workflow_job"] != awxID(wr.ID) {
		t.Errorf("awx id missing: %v", got)
	}
	if got["workflow_job_template"] != awxID("cert-issue") {
		t.Error("a workflow_job must point back at the template it ran")
	}
	sf := got["summary_fields"].(map[string]any)["stratt"].(map[string]any)
	if sf["workflow_run"] != wr.ID {
		t.Errorf("the native WorkflowRun id is the descent link and must survive: %v", sf)
	}
	if sf["triggered_by"] != "cert-reconcile" {
		t.Errorf("what fired this execution is diagnosis, not decoration: %v", sf)
	}
	if got["status"] != "running" || got["failed"] != false {
		t.Errorf("status mapping: %v", got)
	}
}

// A partial cross-Cell outcome reads as failed here, never as successful — the same rule
// mapStatus already holds for jobs, asserted for workflow_jobs because that is the surface an
// operator would otherwise trust.
func TestPartialWorkflowJobIsNotSuccessful(t *testing.T) {
	got := workflowRunToJob(types.WorkflowRun{ID: "x", WorkflowName: "w", Status: types.RunPartial})
	if got["status"] == "successful" || got["failed"] != true {
		t.Errorf("a partial outcome must not render as success: %v", got)
	}
}
