package ansible

import (
	"strings"
	"testing"
)

// TestExtractOutputsIsSeparateFromFacts pins the CERT-2 convention and, more importantly, the
// reason the two keys are separate.
//
// `stratt_facets` is OBSERVED STATE projected onto an Entity under the Run's FacetWriteScope.
// `stratt_outputs` is a value handed to a LATER STEP in the same Workflow. A CSR is the clear case:
// it is not a fact about the host worth keeping in the graph, it is a request the next Step must
// sign — and writing it as a facet would put a short-lived artifact into a projection that outlives
// it, then leave it there when the certificate is reissued.
//
// This is what makes the born-on-target flow expressible at all: the target generates its key and
// CSR, publishes ONLY the CSR, and the private key never crosses the wire (§2.5) — the property
// cert-issuer's input Contract states in writing and that the port could not honour until an
// Actuator Step could hand a value downstream.
func TestExtractOutputsIsSeparateFromFacts(t *testing.T) {
	facts := map[string]any{
		"stratt_outputs": map[string]any{"csr": "-----BEGIN CERTIFICATE REQUEST-----"},
		"stratt_facets":  map[string]any{"app.config": map[string]any{"port": "8080"}},
	}
	out := extractOutputs(facts)
	if !strings.Contains(string(out), "CERTIFICATE REQUEST") {
		t.Fatalf("stratt_outputs must be lifted for cross-Step binding, got %q", out)
	}
	if strings.Contains(string(out), "app.config") {
		t.Fatal("a FACET must not leak into the outputs payload — they go to different places under " +
			"different authority (a facet is gated by FacetWriteScope; an output is bound by a Step)")
	}
	// A play that reports facets and no outputs hands nothing downstream, which is every play
	// shipping today. Absence must be nil rather than an empty object, so the terminal carries no
	// outputs at all and the hub has nothing to govern.
	if got := extractOutputs(map[string]any{"stratt_facets": map[string]any{"app.config": 1}}); got != nil {
		t.Fatalf("no stratt_outputs must yield nil, got %q", got)
	}
}
