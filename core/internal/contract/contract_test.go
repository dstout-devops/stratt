package contract

import (
	"strings"
	"testing"
)

func TestValidateActuatorParams(t *testing.T) {
	// Valid.
	if err := ValidateActuatorParams("script", []byte(`{"script":"echo hi"}`)); err != nil {
		t.Fatalf("valid script params rejected: %v", err)
	}
	if err := ValidateActuatorParams("ansible", []byte(`{}`)); err != nil {
		t.Fatalf("empty ansible params (gather default) rejected: %v", err)
	}
	if err := ValidateActuatorParams("ansible", nil); err != nil {
		t.Fatalf("nil params must validate as {}: %v", err)
	}
	// check arrived with ansible.input v2 (ADR-0019) — the latest version
	// answers validation.
	if err := ValidateActuatorParams("ansible", []byte(`{"check":true}`)); err != nil {
		t.Fatalf("v2 check param rejected: %v", err)
	}

	// The slice-7 e2e failure class: a typoed key, caught with a pointer.
	err := ValidateActuatorParams("script", []byte(`{"soruce":"typo"}`))
	if err == nil {
		t.Fatal("typoed params must be rejected")
	}
	var verr *ValidationError
	if !strings.Contains(err.Error(), "contract actuators/script.input") {
		t.Fatalf("error must name the contract: %v", err)
	}
	_ = verr

	// Missing required.
	if err := ValidateActuatorParams("script", []byte(`{}`)); err == nil {
		t.Fatal("script without source key must be rejected")
	}
	// Wrong type.
	if err := ValidateActuatorParams("ansible", []byte(`{"play":42}`)); err == nil {
		t.Fatal("non-string play must be rejected")
	}
	// Unknown actuator = uncontracted surface.
	if err := ValidateActuatorParams("nonesuch", []byte(`{}`)); err == nil {
		t.Fatal("actuator without a contract must be refused")
	}
}

func TestValidateFacet(t *testing.T) {
	covered, err := ValidateFacet("os.kernel", []byte(`{"family":"linux","release":"6.6","arch":"x86_64"}`))
	if !covered || err != nil {
		t.Fatalf("valid os.kernel: covered=%v err=%v", covered, err)
	}
	covered, err = ValidateFacet("os.kernel", []byte(`{"family":"linux","bogus":true}`))
	if !covered || err == nil {
		t.Fatalf("additionalProperties must be rejected: covered=%v err=%v", covered, err)
	}
	// Undemanded namespace passes uncovered (§1.1).
	covered, err = ValidateFacet("vm.config", []byte(`{"anything":1}`))
	if covered || err != nil {
		t.Fatalf("uncovered namespace must pass: covered=%v err=%v", covered, err)
	}
}

// TestNetSubnetUnionCoFidelity is the BLOCKING cross-plugin co-fidelity gate for the
// shared net.subnet Facet (ADR-0096 guardian flag 2): the closed union schema now
// governs the LIVE write path of BOTH crossplane and awsec2. If either Source's real
// emission stops validating, its projection breaks silently at write time — so this test
// pins both shapes against the SAME ValidateFacet the write path uses.
func TestNetSubnetUnionCoFidelity(t *testing.T) {
	// crossplane's emission (plugins/crossplane/crossplane.go): {claim, name, cidr}.
	if covered, err := ValidateFacet("net.subnet", []byte(`{"claim":"SubnetClaim","name":"web","cidr":"10.0.0.0/24"}`)); !covered || err != nil {
		t.Fatalf("crossplane net.subnet emission must validate: covered=%v err=%v", covered, err)
	}
	// awsec2's emission (plugins/awsec2/normalize_resources.go): {cidr, availabilityZone, state, vpcId}.
	if covered, err := ValidateFacet("net.subnet", []byte(`{"cidr":"10.0.1.0/24","availabilityZone":"us-east-1a","state":"available","vpcId":"vpc-1"}`)); !covered || err != nil {
		t.Fatalf("awsec2 net.subnet emission must validate: covered=%v err=%v", covered, err)
	}
	// A field no Source emits is rejected (the schema stays closed — drift is blocking).
	if covered, err := ValidateFacet("net.subnet", []byte(`{"cidr":"10.0.0.0/24","undeclared":true}`)); !covered || err == nil {
		t.Fatalf("net.subnet must reject undeclared keys (closed): covered=%v err=%v", covered, err)
	}
}

func TestPinsAreStable(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 96 {
		t.Fatalf("expected 96 embedded documents, got %d", len(all))
	}
	versions := map[string]int{}
	for _, c := range all {
		if len(c.Hash) != 64 || c.Rung != "hand-written" || c.Version < 1 {
			t.Fatalf("pin shape: %+v", c)
		}
		if c.Version > versions[c.Name] {
			versions[c.Name] = c.Version
		}
	}
	// ansible.input v4 (extraVars, ADR-0026) resolves as the current version;
	// v1/v2/v3 stay pinned alongside it.
	if versions["actuators/ansible.input"] != 4 {
		t.Fatalf("ansible.input current version: %d", versions["actuators/ansible.input"])
	}
	// Same process, same documents → identical pins on re-read.
	again, _ := All()
	for i := range all {
		if all[i].Hash != again[i].Hash {
			t.Fatal("hashes must be deterministic")
		}
	}
}

// TestActionContracts covers the Action input/output validation direction
// (§2.2, ADR-0031) — the direction that distinguishes an Action from an Actuator.
func TestActionContracts(t *testing.T) {
	// Input: a valid put-bucket-policy; a missing required field; an unknown action.
	if err := ValidateActionInput("awss3/put-bucket-policy", []byte(`{"name":"b","policy":"{}"}`)); err != nil {
		t.Fatalf("valid put-bucket-policy input: %v", err)
	}
	if err := ValidateActionInput("awss3/put-bucket-policy", []byte(`{"name":"b"}`)); err == nil {
		t.Fatal("put-bucket-policy input missing policy must be rejected")
	}
	if err := ValidateActionInput("awss3/nope", []byte(`{}`)); err == nil {
		t.Fatal("an uncontracted action must be refused")
	}
	// Output: a valid create-bucket output; a bad one (missing bucketArn) rejected.
	if err := ValidateActionOutput("awss3/create-bucket", []byte(`{"bucketArn":"arn:aws:s3:::b"}`)); err != nil {
		t.Fatalf("valid create-bucket output: %v", err)
	}
	if err := ValidateActionOutput("awss3/create-bucket", []byte(`{"name":"x"}`)); err == nil {
		t.Fatal("create-bucket output missing bucketArn must fail validation (§1.8)")
	}
	if err := ValidateActionOutput("awsec2/create-vm", []byte(`{"instanceId":"i-1","privateIp":"10.0.0.1"}`)); err != nil {
		t.Fatalf("valid create-vm output: %v", err)
	}
}
