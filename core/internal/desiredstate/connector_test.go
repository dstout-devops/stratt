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
	var haveConn, haveAct bool
	for _, c := range d.Connectors {
		if c.Name == "declared" && c.Class == "syncer" && c.Source.Name == "declared-dev" {
			haveConn = true
		}
	}
	for _, a := range d.Actuators {
		if a.Name == "helm" && len(a.ActionNames) == 1 && a.ActionNames[0] == "helm/deploy" {
			haveAct = true
		}
	}
	if !haveConn {
		t.Fatalf("estate/connectors/declared.yaml must parse into the declared Connector; got %+v", d.Connectors)
	}
	if !haveAct {
		t.Fatalf("estate/actuators/helm.yaml must parse into the helm Actuator; got %+v", d.Actuators)
	}
}
