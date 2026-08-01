package contract

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestSingletonIntentKindsShareOneDeclarationSeam refuses a named-singleton Intent
// kind whose spec schema still carries the provider-coupled seam ADR-0110 replaced.
//
// ADR-0110 D1 moved every singleton Intent off `builder:`/`buildWorkflow:` and onto
// `requires: [provisioning]` — the Intent names a capability CLASS, the reconcile
// resolves the bound provider. It shipped subnet.v2, vlan.v2 and dmz.v2 to say so.
// **dnsrecord never got a v2**, and the cost was not cosmetic: v1 REQUIRES the two
// removed fields and closes with additionalProperties:false, so an Intent/DnsRecord
// written the way every other singleton in the estate is written FAILED VALIDATION,
// and one written the old way decoded into a SingletonSpec with an empty Requires
// that resolved to no provider at all. The kind was not merely unused — it was
// undeclarable, for two ADRs, with nothing to say so (ADR-0144 D6).
//
// The family is still growing (ADR-0059 → 0110 → 0111/0112), so the check is derived
// from types.SingletonIntentKinds rather than a literal list: a kind added to that map
// inherits this guard instead of needing someone to remember it.
func TestSingletonIntentKindsShareOneDeclarationSeam(t *testing.T) {
	if len(types.SingletonIntentKinds) == 0 {
		t.Fatal("no singleton Intent kinds — the layout changed and this guard is checking nothing")
	}
	kinds := make([]string, 0, len(types.SingletonIntentKinds))
	for k := range types.SingletonIntentKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			// The EFFECTIVE schema for the kind — the highest version, which is what
			// ValidateIntentSpec actually enforces. Checking the raw v1 document would
			// report a false failure the moment a v2 lands, and checking only that a v2
			// EXISTS would miss a v2 that kept the old fields.
			if err := ensure(); err != nil {
				t.Fatal(err)
			}
			c, ok := intentSet[kind]
			if !ok {
				t.Fatalf("%s is a declared singleton Intent kind with no spec schema — "+
					"a kind is implemented exactly when its schema exists (§1.1)", kind)
			}
			var doc struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(c.contract.Schema, &doc); err != nil {
				t.Fatalf("%s: decode schema: %v", kind, err)
			}
			for _, gone := range []string{"builder", "buildWorkflow"} {
				if _, present := doc.Properties[gone]; present {
					t.Errorf("%s (%s v%d) still declares %q — ADR-0110 D1 replaced the provider-coupled "+
						"builder seam with `requires: [provisioning]`; a kind left on the old one is "+
						"UNDECLARABLE in the estate's own idiom, not merely out of date",
						kind, c.contract.Name, c.contract.Version, gone)
				}
			}
			if _, ok := doc.Properties["requires"]; !ok {
				t.Errorf("%s (%s v%d) declares no `requires` — the capability CLASS is how a singleton "+
					"Intent names what builds it (ADR-0110 D1/§1.5), and provision.SingletonSpec reads "+
					"exactly that field; without it the Intent resolves to no provider",
					kind, c.contract.Name, c.contract.Version)
			}
			if !listHas(doc.Required, "requires") {
				t.Errorf("%s (%s v%d) does not REQUIRE `requires` — an optional one admits an Intent that "+
					"parses, reconciles, and silently builds nothing (§1.8)",
					kind, c.contract.Name, c.contract.Version)
			}
		})
	}
}

func listHas(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
