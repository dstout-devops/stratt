package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestParseConnectorFile(t *testing.T) {
	yaml := `
name: declared-dev
class: syncer
address: stratt-declared:9090
pluginIdentity: declared
tier: trusted
source:
  kind: declared
  name: declared-dev
  endpoint: file:///hosts
facetNamespaces: [mgmt.address]
authoritativeFacetNamespaces: [mgmt.address]
labelKeys: [os, role]
identitySchemes: [dns.fqdn]
tombstoneSchemes: [dns.fqdn]
intervalSeconds: 15
`
	name, c, err := parseConnectorFile("declared.yaml", []byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if name != "declared-dev" || c.Class != types.ConnectorSyncer || c.Address != "stratt-declared:9090" ||
		c.Source.Kind != "declared" || c.IntervalSeconds != 15 {
		t.Fatalf("bad parse: %+v", c)
	}
}

// TestConnectorRejectsHoming is the load-bearing §2.4 check (ADR-0103): a Connector must not
// declare its Source's runtime placement — that is the home-gate's single-writer domain.
func TestConnectorRejectsHoming(t *testing.T) {
	base := `
name: x
class: syncer
address: a:9090
pluginIdentity: x
source:
  kind: x
  name: x
`
	for _, homing := range []string{"  cell: cell-b", "  rehomingTo: cell-b", "  homeEpoch: 3"} {
		_, _, err := parseConnectorFile("x.yaml", []byte(base+homing+"\n"))
		if err == nil || !strings.Contains(err.Error(), "runtime placement is not CaC") {
			t.Fatalf("a Connector setting %q must be rejected (§2.4), got err=%v", strings.TrimSpace(homing), err)
		}
	}
}

func TestConnectorValidation(t *testing.T) {
	bad := []types.Connector{
		{Name: "", Class: "syncer", Address: "a", PluginIdentity: "p", Source: types.Source{Name: "s"}}, // no name
		{Name: "n", Class: "bogus", Address: "a", PluginIdentity: "p", Source: types.Source{Name: "s"}}, // bad class
		{Name: "n", Class: "syncer", Address: "", PluginIdentity: "p", Source: types.Source{Name: "s"}}, // no address
		{Name: "n", Class: "syncer", Address: "a", PluginIdentity: "p", Source: types.Source{Name: ""}}, // no source name
		{Name: "n", Class: "syncer", Address: "a", PluginIdentity: "p", Source: types.Source{Name: "s"}, // authoritative ⊄ facets
			AuthoritativeFacetNamespaces: []string{"ns.x"}},
	}
	for i, c := range bad {
		if err := ValidateConnector(c); err == nil {
			t.Fatalf("invalid connector[%d] must be rejected: %+v", i, c)
		}
	}
}

// TestCapabilityVocabulary is the ADR-0104 §1.5 gate: a plugin never mints a capability's
// meaning — an unknown provides/requires token is rejected at admission on both Kinds.
func TestCapabilityVocabulary(t *testing.T) {
	// A known capability validates on both Kinds.
	if err := ValidateConnector(types.Connector{Name: "n", Class: "syncer", Address: "a", PluginIdentity: "p", Source: types.Source{Name: "s"}, Requires: []string{"keycustodian"}}); err != nil {
		t.Fatalf("a known capability must validate: %v", err)
	}
	if err := ValidateActuator(types.Actuator{Name: "n", PluginIdentity: "p", Address: "a", Provides: []string{"statestore"}}); err != nil {
		t.Fatalf("a known capability must validate: %v", err)
	}
	// An unknown token is rejected in provides and in requires, on both Kinds.
	if err := ValidateConnector(types.Connector{Name: "n", Class: "syncer", Address: "a", PluginIdentity: "p", Source: types.Source{Name: "s"}, Requires: []string{"durableexec"}}); err == nil {
		t.Fatal("durableexec is spine, not a requirable capability (ADR-0104 D6) — must be rejected")
	}
	if err := ValidateActuator(types.Actuator{Name: "n", PluginIdentity: "p", Address: "a", Provides: []string{"bogus"}}); err == nil {
		t.Fatal("an unknown provides token must be rejected (core-owned vocabulary, §1.5)")
	}
}

func TestParseActuatorFile(t *testing.T) {
	yaml := `
name: helm
address: stratt-helm:9090
pluginIdentity: helm
tier: trusted
dryRunnable: true
actionNames: [helm/deploy]
`
	name, a, err := parseActuatorFile("helm.yaml", []byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if name != "helm" || a.Address != "stratt-helm:9090" || !a.DryRunnable || len(a.ActionNames) != 1 {
		t.Fatalf("bad parse: %+v", a)
	}
}

func TestActuatorValidation(t *testing.T) {
	// no transport, and both transports, are each rejected (exactly one required).
	if err := ValidateActuator(types.Actuator{Name: "n", PluginIdentity: "p"}); err == nil {
		t.Fatal("an actuator with neither address nor jobCommand must be rejected")
	}
	if err := ValidateActuator(types.Actuator{Name: "n", PluginIdentity: "p", Address: "a", JobCommand: []string{"x"}}); err == nil {
		t.Fatal("an actuator with both address and jobCommand must be rejected")
	}
	if err := ValidateActuator(types.Actuator{Name: "n", PluginIdentity: "p", JobCommand: []string{"stratt-ansible"}}); err != nil {
		t.Fatalf("an EE-Job actuator (jobCommand only) must validate: %v", err)
	}
}

// TestParseRealEstate validates the migrated estate declarations (ADR-0103 S7) parse through
// the full ParseDir the daemon uses.
func TestParseRealEstate(t *testing.T) {
	d, err := ParseDir("../../../estate", nil)
	if err != nil {
		t.Fatalf("parse /estate: %v", err)
	}
	var haveConn, haveAct, haveProvider bool
	for _, c := range d.Connectors {
		if c.Name == "declared" && c.Class == "syncer" && c.Source.Name == "declared-dev" {
			haveConn = true
		}
	}
	for _, a := range d.Actuators {
		if a.Name == "helm" && len(a.ActionNames) == 1 && a.ActionNames[0] == "helm/deploy" {
			haveAct = true
		}
		// s3-statestore is the ADR-0105 statestore capability provider (provides:[statestore]).
		if a.Name == "s3-statestore" && len(a.Provides) == 1 && a.Provides[0] == "statestore" &&
			len(a.ActionNames) == 1 && a.ActionNames[0] == "awss3/statestore-resolve" {
			haveProvider = true
		}
	}
	if !haveProvider {
		t.Fatalf("estate/actuators/s3-statestore.yaml must parse into a statestore provider; got %+v", d.Actuators)
	}
	// openbao is the ADR-0106 multi-capability provider (provides:[keycustodian, certissuer]).
	var haveOpenBao bool
	for _, a := range d.Actuators {
		if a.Name == "openbao" && len(a.Provides) == 2 &&
			a.Provides[0] == "keycustodian" && a.Provides[1] == "certissuer" && len(a.ActionNames) == 0 {
			haveOpenBao = true
		}
	}
	if !haveOpenBao {
		t.Fatalf("estate/actuators/openbao.yaml must parse into a keycustodian+certissuer provider (no resolve Action); got %+v", d.Actuators)
	}
	// opentofu-s3 is the first real `requires:` CONSUMER (ADR-0105 D4): requires:[statestore].
	var haveConsumer bool
	for _, a := range d.Actuators {
		if a.Name == "opentofu-s3" && len(a.Requires) == 1 && a.Requires[0] == "statestore" {
			haveConsumer = true
		}
	}
	if !haveConsumer {
		t.Fatalf("estate/actuators/opentofu-s3.yaml must parse into a statestore consumer (requires:[statestore]); got %+v", d.Actuators)
	}
	// awsec2 is the ADR-0107 provisioning provider (provides:[provisioning], enablement-gate).
	var haveEC2 bool
	for _, a := range d.Actuators {
		if a.Name == "awsec2" && len(a.Provides) == 1 && a.Provides[0] == "provisioning" && len(a.ActionNames) == 0 {
			haveEC2 = true
		}
	}
	if !haveEC2 {
		t.Fatalf("estate/actuators/awsec2.yaml must parse into a provisioning provider; got %+v", d.Actuators)
	}
	if !haveConn {
		t.Fatalf("estate/connectors/declared.yaml must parse into the declared Connector; got %+v", d.Connectors)
	}
	if !haveAct {
		t.Fatalf("estate/actuators/helm.yaml must parse into the helm Actuator; got %+v", d.Actuators)
	}
}
