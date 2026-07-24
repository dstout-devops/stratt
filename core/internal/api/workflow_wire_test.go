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
