package desiredstate

import (
	"context"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/types"
)

// TestAdmitDeclarations proves the imperative-door admission PEP (GOV-2): a
// declaration POSTed straight to the API is admitted through the PDP port, the
// same as the Git reconcile — a denying control rejects it, and the object shape
// (kind + typed fields) is what the CEL predicate sees.
func TestAdmitDeclarations(t *testing.T) {
	dec := policy.CEL{}
	// Deny any Workflow named with a "forbidden-" prefix (a real admission shape).
	controls := []types.Control{{
		ID:      "no-forbidden-workflows",
		When:    `object.kind == "Workflow" && object.name.startsWith("forbidden-")`,
		Outcome: types.OutcomeDeny,
	}}
	if err := policy.ValidateAdmissionControls(controls); err != nil {
		t.Fatalf("admission controls should validate: %v", err)
	}

	t.Run("denied declaration is rejected", func(t *testing.T) {
		decls := Declarations{Workflows: []types.Workflow{{Name: "forbidden-drop-prod"}}}
		err := AdmitDeclarations(context.Background(), decls, controls, dec)
		if err == nil {
			t.Fatal("a forbidden Workflow must be denied at the imperative door")
		}
		if !strings.Contains(err.Error(), "admission denied") {
			t.Fatalf("want an admission-denied error, got: %v", err)
		}
	})

	t.Run("allowed declaration passes", func(t *testing.T) {
		decls := Declarations{Workflows: []types.Workflow{{Name: "build-app"}}}
		if err := AdmitDeclarations(context.Background(), decls, controls, dec); err != nil {
			t.Fatalf("a non-forbidden Workflow must pass: %v", err)
		}
	})

	t.Run("nil decider is a no-op", func(t *testing.T) {
		decls := Declarations{Workflows: []types.Workflow{{Name: "forbidden-anything"}}}
		if err := AdmitDeclarations(context.Background(), decls, controls, nil); err != nil {
			t.Fatalf("nil decider must skip admission, got: %v", err)
		}
	})

	t.Run("empty policy is a no-op", func(t *testing.T) {
		decls := Declarations{Workflows: []types.Workflow{{Name: "forbidden-anything"}}}
		if err := AdmitDeclarations(context.Background(), decls, nil, dec); err != nil {
			t.Fatalf("empty controls must skip admission, got: %v", err)
		}
	})

	t.Run("intent keeps its own sub-kind", func(t *testing.T) {
		// An Intent's own kind (Certificate) must win over the fallback, so a
		// control keyed on object.kind sees the real sub-kind (mirrors the Git door).
		kindCtl := []types.Control{{
			ID:      "no-cert-intents",
			When:    `object.kind == "Certificate"`,
			Outcome: types.OutcomeDeny,
		}}
		decls := Declarations{Intents: []types.Intent{{Name: "web-tls", Kind: "Certificate"}}}
		if err := AdmitDeclarations(context.Background(), decls, kindCtl, dec); err == nil {
			t.Fatal("a Certificate Intent must match object.kind == \"Certificate\"")
		}
	})
}
