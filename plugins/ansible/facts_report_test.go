package ansible

import (
	"encoding/json"
	"testing"
)

// TestExtractFactsGenericReport pins the ADR-0084 fact-back convention: a play
// projects ANY Facet via the reserved `stratt_facets` map. The remediation reports
// the observed app.config.port back, which lets the drift Finding resolve.
func TestExtractFactsGenericReport(t *testing.T) {
	ev := RunnerEvent{
		Event: "runner_on_ok",
		EventData: map[string]any{
			"res": map[string]any{
				"ansible_facts": map[string]any{
					"stratt_facets": map[string]any{
						"app.config": map[string]any{"port": "8080"},
					},
				},
			},
		},
	}
	out := extractFacts(ev)
	raw, ok := out["app.config"]
	if !ok {
		t.Fatalf("stratt_facets must project app.config, got %v", out)
	}
	var got struct {
		Port string `json:"port"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Port != "8080" {
		t.Fatalf("app.config value wrong: %s (err %v)", raw, err)
	}
}
