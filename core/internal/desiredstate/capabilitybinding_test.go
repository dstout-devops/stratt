package desiredstate

import (
	"strings"
	"testing"
)

// A well-formed capability-binding parses and validates, and its entries round-trip.
func TestParseCapabilityBinding_Valid(t *testing.T) {
	raw := []byte(`
name: provisioning-dev
entries:
  - capability: provisioning
    provider: awsec2
    intentKind: Compute
  - capability: provisioning
    provider: crossplane
    intentKind: Vlan
`)
	name, b, err := parseCapabilityBindingFile("capability-bindings/dev.yaml", raw)
	if err != nil {
		t.Fatalf("valid binding must parse: %v", err)
	}
	if name != "provisioning-dev" {
		t.Fatalf("name = %q, want provisioning-dev", name)
	}
	if len(b.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(b.Entries))
	}
	if b.Entries[1].Provider != "crossplane" || b.Entries[1].IntentKind != "Vlan" {
		t.Fatalf("entry[1] = %+v, want {crossplane, Vlan}", b.Entries[1])
	}
}

func TestValidateCapabilityBinding_Rejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring the error must contain
	}{
		{
			name: "unknown capability",
			yaml: "name: b\nentries:\n  - capability: bogus\n    provider: awsec2\n    intentKind: Compute\n",
			want: "unknown capability",
		},
		{
			name: "Intent/ prefix rejected",
			yaml: "name: b\nentries:\n  - capability: provisioning\n    provider: awsec2\n    intentKind: Intent/Compute\n",
			want: "must omit the Intent/ prefix",
		},
		{
			name: "missing provider",
			yaml: "name: b\nentries:\n  - capability: provisioning\n    intentKind: Compute\n",
			want: "provider is required",
		},
		{
			name: "within-document duplicate (class, kind)",
			yaml: "name: b\nentries:\n  - capability: provisioning\n    provider: awsec2\n    intentKind: Subnet\n  - capability: provisioning\n    provider: crossplane\n    intentKind: Subnet\n",
			want: "duplicate",
		},
		{
			name: "no entries",
			yaml: "name: b\nentries: []\n",
			want: "at least one entry",
		},
		{
			name: "unknown yaml field",
			yaml: "name: b\nbogusField: x\nentries:\n  - capability: provisioning\n    provider: awsec2\n    intentKind: Compute\n",
			want: "bogusField",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseCapabilityBindingFile("capability-bindings/bad.yaml", []byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected rejection for %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must contain %q", err.Error(), tc.want)
			}
		})
	}
}
