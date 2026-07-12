package opentofu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactPlan(t *testing.T) {
	plan := json.RawMessage(`{
		"resource_changes": [{"change": {
			"after": {"password": "hunter2", "name": "web"},
			"after_sensitive": {"password": true},
			"before": null, "before_sensitive": false
		}}],
		"output_changes": {"admin": {"after": "top-secret", "after_sensitive": true}},
		"planned_values": {
			"outputs": {"admin": {"sensitive": true, "value": "top-secret"}},
			"root_module": {"resources": [{"values": {"token": "abc123"}, "sensitive_values": {"token": true}}],
				"child_modules": [{"resources": [{"values": {"key": "k-9"}, "sensitive_values": {"key": true}}]}]}
		}
	}`)
	out := string(redactPlan(plan))
	for _, leaked := range []string{"hunter2", "top-secret", "abc123", "k-9"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("sensitive value %q leaked:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, `"name":"web"`) {
		t.Fatalf("non-sensitive values must survive:\n%s", out)
	}
}

func TestReservedLabelPrefixRejected(t *testing.T) {
	_, _, err := interpretOutputs(json.RawMessage(`{
		"stratt_entities":{"sensitive":false,"type":"string",
			"value":[{"kind":"endpoint","identityKeys":{"x":"1"},"labels":{"stratt.workspace":"spoof"}}]}
	}`))
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved prefix must be rejected: %v", err)
	}
}
