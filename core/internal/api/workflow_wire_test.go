package api

import "testing"

// A targetless Action Workflow step (e.g. helm/deploy — no View) must survive the
// desired-state wire round-trip. Regression guard for the gap the k8s-deploy demo
// surfaced (ADR-0116): `stratt apply` marshals to the wire DesiredState and the
// server re-parses via workflowFromWire — if `action` is dropped there, the server
// sees an actuation step with no viewName and rejects it ("actuation step requires
// viewName"), even though the Git/reconcile path accepts the same Workflow. §1.6:
// every capability identical across surfaces; the API door must carry what Git does.
func TestWorkflowWireRoundTripTargetlessAction(t *testing.T) {
	in := Workflow{
		Name: "deploy-hello",
		Steps: []Step{
			{Name: "approve", Gate: &GateSpec{Approvers: GateApprovers{Teams: ptr([]string{"platform-admins"})}}},
			{
				Name:           "deploy",
				Needs:          ptr([]string{"approve"}),
				Action:         ptr("helm/deploy"),
				CredentialRefs: ptr([]string{"helm-deploy"}),
				Params: ptr(map[string]any{
					"chart":     "hello-stratt",
					"release":   "hello",
					"namespace": "demo",
				}),
			},
		},
	}

	// fromWire validates too: a dropped Action would fail ValidateWorkflow here.
	got, err := workflowFromWire(in)
	if err != nil {
		t.Fatalf("workflowFromWire: %v", err)
	}
	deploy := got.Steps[1]
	if deploy.Action != "helm/deploy" {
		t.Fatalf("Action not carried through wire: got %q, want %q", deploy.Action, "helm/deploy")
	}
	if deploy.ViewName != "" || deploy.Actuator != "" {
		t.Fatalf("targetless Action step gained a View/Actuator: viewName=%q actuator=%q", deploy.ViewName, deploy.Actuator)
	}

	// And back out: the reverse mapping must preserve it for GET/round-trip parity.
	if a := stepToWire(got.Steps[1]).Action; a == nil || *a != "helm/deploy" {
		t.Fatalf("stepToWire dropped Action: %v", a)
	}
}

// The XOR must be enforced AT the wire boundary (via ValidateWorkflow inside
// workflowFromWire), not only in the domain package — an action+viewName step is
// contradictory and must be rejected on the API door.
func TestWorkflowWireRejectsActionAndActuation(t *testing.T) {
	in := Workflow{
		Name: "bad",
		Steps: []Step{{
			Name:     "deploy",
			Action:   ptr("helm/deploy"),
			ViewName: ptr("some-view"),
			Actuator: ptr("helm"),
		}},
	}
	if _, err := workflowFromWire(in); err == nil {
		t.Fatal("expected wire path to reject a step that is both an action and an actuation")
	}
}

// A gateOnly (material-less) CredentialRef must survive the wire the same way the
// Git/ParseDir door accepts it — regression guard for the k8s-deploy demo's
// helm-deploy ref (ADR-0116/0052/0092). Empty injection is legal ONLY with
// gateOnly:true; empty-without-gateOnly and gateOnly-with-injection both reject.
func TestCredentialRefWireGateOnly(t *testing.T) {
	gate := true
	ok := CredentialRef{
		Name: "helm-deploy", OwnerTeam: "platform-admins",
		Backend: "k8s-secret", Locator: map[string]any{"namespace": "stratt-authz", "name": "helm-deploy-authz"},
		Injection: []CredentialInjection{}, GateOnly: &gate,
	}
	got, err := credentialRefFromWire(ok)
	if err != nil {
		t.Fatalf("gateOnly ref rejected over wire: %v", err)
	}
	if !got.GateOnly || len(got.Injection) != 0 {
		t.Fatalf("gateOnly not carried: GateOnly=%v injection=%d", got.GateOnly, len(got.Injection))
	}
	if g := credentialRefToWire(got).GateOnly; g == nil || !*g {
		t.Fatalf("credentialRefToWire dropped gateOnly: %v", g)
	}

	// Empty injection WITHOUT gateOnly must reject (the §1.8 accidental-drop guard).
	bare := ok
	bare.GateOnly = nil
	if _, err := credentialRefFromWire(bare); err == nil {
		t.Fatal("expected empty-injection ref without gateOnly to be rejected")
	}

	// gateOnly WITH an injection block is contradictory — reject.
	contradictory := ok
	contradictory.Injection = []CredentialInjection{{Key: "token", As: "env", Name: "TOKEN"}}
	if _, err := credentialRefFromWire(contradictory); err == nil {
		t.Fatal("expected gateOnly ref with a non-empty injection block to be rejected")
	}
}

func ptr[T any](v T) *T { return &v }

// TestWorkflowWireRoundTripInputs is the same bug class as the targetless-Action case
// above, one field along: the launch interface must survive the wire.
//
// It is not merely cosmetic. ValidateWorkflow checks every {{.launch.x}} binding against
// the DECLARED inputs (ADR-0118 D2), so if `inputs` were dropped in workflowFromWire the
// server would reject a perfectly valid Workflow — reporting that it binds an undeclared
// input while the Git/reconcile path accepts the identical document. A false rejection that
// only appears on one surface is exactly the §1.6 asymmetry the precedent above was written
// for.
func TestWorkflowWireRoundTripInputs(t *testing.T) {
	inputs := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"targetSubnet"},
		"properties": map[string]any{
			"targetSubnet": map[string]any{"type": "string"},
		},
	}
	in := Workflow{
		Name:   "subnet-provision",
		Inputs: &inputs,
		Steps: []Step{
			{Name: "approve", Gate: &GateSpec{Approvers: GateApprovers{Teams: ptr([]string{"platform-admins"})}}},
			{
				Name:  "build",
				Needs: ptr([]string{"approve"}),
				// A real, contracted Action. This fixture used `script`, which is an
				// ACTUATOR (contracts/actuators/script.input) with an actuator param
				// shape — as an `action:` it has no input Contract at all and would have
				// failed at launch. The load now refuses that, which is what caught it.
				Action: ptr("crossplane/provision"),
				Params: &map[string]any{"claimName": "{{.launch.targetSubnet}}"},
			},
		},
	}
	got, err := workflowFromWire(in)
	if err != nil {
		t.Fatalf("a Workflow declaring inputs must survive the wire: %v", err)
	}
	if len(got.Inputs) == 0 {
		t.Fatal("inputs were dropped — a {{.launch.x}} binding would then be falsely rejected")
	}
	// And the round trip is symmetric, so GET returns what was applied.
	back := workflowToWire(got)
	if back.Inputs == nil {
		t.Fatal("workflowToWire must publish inputs; without it no surface can generate a launch form")
	}
	if (*back.Inputs)["additionalProperties"] != false {
		t.Fatalf("the closed-world flag must round-trip intact, got %v", (*back.Inputs)["additionalProperties"])
	}
}

// ── the remediation door's binding rule (ADR-0118 D3) ─────────────────────────────────

// TestRemediationInputsHaveOneBindingSite: the compiled params and a caller's body are two
// independent sources for one value. Merging them would need a precedence rule — "the caller
// wins" or "the route wins" — and that is the implicit precedence §2.4 forbids, at a WORSE
// boundary than the Intent layer: resolved at run time, with no declaration to read
// afterwards to explain which value was used.
//
// So the overlap is refused. A caller may still fill inputs the route leaves unset, which is
// what makes the door usable for a Workflow whose interface is only partly compiled.
func TestRemediationInputsHaveOneBindingSite(t *testing.T) {
	compiled := map[string]any{"port": "8443", "channel": "stable"}

	// Filling an input the route leaves unset is allowed and merges.
	merged, clashes := mergeRemediationInputs(compiled, map[string]any{"reason": "incident-42"})
	if len(clashes) != 0 {
		t.Fatalf("supplying an input the route does not set must be allowed, got clashes %v", clashes)
	}
	if merged["reason"] != "incident-42" || merged["port"] != "8443" {
		t.Fatalf("merge must keep both sources' distinct keys, got %#v", merged)
	}

	// Contradicting a compiled input is refused, and every offending key is named.
	_, clashes = mergeRemediationInputs(compiled, map[string]any{"port": "9999", "channel": "beta"})
	if len(clashes) != 2 || clashes[0] != "channel" || clashes[1] != "port" {
		t.Fatalf("both clashing keys must be reported, sorted; got %v", clashes)
	}

	// No compiled params at all: the caller supplies everything, nothing clashes.
	merged, clashes = mergeRemediationInputs(nil, map[string]any{"port": "443"})
	if len(clashes) != 0 || merged["port"] != "443" {
		t.Fatalf("with no compiled params the caller is the only binding site; got %#v %v", merged, clashes)
	}
}

// TestTriggerWireRoundTripInputs: a Workflow-target Trigger's launch inputs must survive the
// wire, for the same reason a Workflow's own inputs must (ADR-0118 D5).
//
// If the API path stripped them, `stratt apply` and the Git/reconcile path would disagree about
// the same document: one would launch the Workflow parameterized, the other would launch it with
// nothing and fail on a required input. A one-surface difference in behaviour, which is the §1.6
// asymmetry this file's first test was written for.
func TestTriggerWireRoundTripInputs(t *testing.T) {
	inputs := map[string]any{"targetSubnet": "app-subnet"}
	in := Trigger{
		Name:         "nightly",
		Kind:         TriggerKind("schedule"),
		Cron:         ptr("0 2 * * *"),
		WorkflowName: ptr("subnet-provision"),
		Principal:    ptr("svc"),
		Inputs:       &inputs,
	}
	got, err := triggerFromWire(in)
	if err != nil {
		t.Fatalf("a Trigger with launch inputs must survive the wire: %v", err)
	}
	if got.Inputs["targetSubnet"] != "app-subnet" {
		t.Fatalf("inputs were dropped or mangled: %#v", got.Inputs)
	}
	back := triggerToWire(got)
	if back.Inputs == nil || (*back.Inputs)["targetSubnet"] != "app-subnet" {
		t.Fatalf("triggerToWire must publish inputs, got %#v", back.Inputs)
	}
}

// The class form has the SAME wire hazard the named form had (the gap the k8s-deploy demo
// surfaced): if `actionCapability` is dropped in workflowFromWire, the server sees a Step that
// is neither an Action nor an actuation and rejects a declaration Git accepts. Worse than the
// original, because a silently-dropped class turns a valid Step into "actuation step requires
// viewName" — a diagnostic pointing at a field the author never wrote (§1.6, §1.8).
func TestWorkflowWireRoundTripActionCapability(t *testing.T) {
	in := Workflow{
		Name: "allocate-subnet",
		Steps: []Step{{
			Name:             "allocate",
			ActionCapability: ptr("ipam"),
			CredentialRefs:   ptr([]string{"netbox-token"}),
			Params: ptr(map[string]any{
				"key":  "web-prod",
				"pool": "10.0.0.0/8",
				"size": 24,
			}),
		}},
	}

	got, err := workflowFromWire(in)
	if err != nil {
		t.Fatalf("workflowFromWire: %v", err)
	}
	step := got.Steps[0]
	if step.ActionCapability != "ipam" {
		t.Fatalf("ActionCapability not carried through wire: got %q", step.ActionCapability)
	}
	if step.Action != "" {
		t.Fatalf("the wire must not resolve the class to a provider: got %q", step.Action)
	}
	if a := stepToWire(step).ActionCapability; a == nil || *a != "ipam" {
		t.Fatalf("stepToWire dropped ActionCapability: %v", a)
	}
}
