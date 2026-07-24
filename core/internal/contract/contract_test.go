package contract

import (
	"strings"
	"testing"
)

// TestIpamResolveActionContractCoFidelity is the ADR-0113 D4 drift guard: the Workflow-facing
// actions/netbox/ipam-resolve.{input,output} Contracts are INTENTIONALLY identical in shape to the
// class-level capabilities/ipam.{input,output} (ADR-0111). They exist because a capability-resolve
// Action invoked as an explicit Workflow Step is validated by the actions/<name> convention, while
// resolve-inject validates the same shape against the class contract. This test binds them so they
// cannot drift: the same representative payloads must validate (and fail) identically against both.
func TestIpamResolveActionContractCoFidelity(t *testing.T) {
	pairs := []struct{ action, class string }{
		{"actions/netbox/ipam-resolve.input", "capabilities/ipam.input"},
		{"actions/netbox/ipam-resolve.output", "capabilities/ipam.output"},
	}
	samples := map[string][]struct {
		payload string
		valid   bool
	}{
		"input": {
			{`{"key":"dmz-subnet-01","role":"dmz","size":24,"vlanGroup":"dc1"}`, true},
			{`{"key":"x","pool":"10.30.0.0/16","size":24}`, true},
			{`{"role":"dmz","size":24}`, false},                             // missing key
			{`{"key":"x","size":24,"pool":"p","role":"r"}`, false},          // pool XOR role
			{`{"key":"x","role":"dmz","size":24,"undeclared":true}`, false}, // closed
		},
		"output": {
			{`{"cidr":"10.30.4.0/24","vlanId":1234}`, true},
			{`{"vlanId":1234}`, false},                           // missing cidr
			{`{"cidr":"10.30.4.0/24","vlanId":9999}`, false},     // vlan out of range
			{`{"cidr":"10.30.4.0/24","undeclared":true}`, false}, // closed
		},
	}
	for _, p := range pairs {
		kind := "input"
		if strings.HasSuffix(p.action, ".output") {
			kind = "output"
		}
		for _, s := range samples[kind] {
			actErr := ValidateNamed(p.action, []byte(s.payload))
			clsErr := ValidateNamed(p.class, []byte(s.payload))
			if (actErr == nil) != s.valid {
				t.Errorf("%s: payload %s expected valid=%v, got err=%v", p.action, s.payload, s.valid, actErr)
			}
			if (actErr == nil) != (clsErr == nil) {
				t.Errorf("co-fidelity drift: %s and %s disagree on %s (action err=%v, class err=%v)", p.action, p.class, s.payload, actErr, clsErr)
			}
		}
	}
}

// TestStatestoreOutputContract is the co-fidelity guard for ADR-0105: the class-level
// capabilities/statestore.output Contract accepts a representative provider-agnostic backend-config
// handle (the shape awss3/statestore-resolve produces) and rejects a malformed one.
func TestStatestoreOutputContract(t *testing.T) {
	ok := []byte(`{"backend":"s3","config":{"bucket":"tfstate","key":"stratt/web-prod.tfstate","region":"eu-west-1","use_lockfile":"true"},"credentialRef":"cred/awss3/state"}`)
	if err := ValidateNamed("capabilities/statestore.output", ok); err != nil {
		t.Fatalf("a valid statestore handle must validate: %v", err)
	}
	// A non-string config value violates the provider-agnostic string-map contract.
	bad := []byte(`{"backend":"s3","config":{"use_lockfile":true}}`)
	if err := ValidateNamed("capabilities/statestore.output", bad); err == nil {
		t.Fatal("a non-string config value must be rejected (config is a string map)")
	}
	// Missing the required backend type.
	if err := ValidateNamed("capabilities/statestore.output", []byte(`{"config":{}}`)); err == nil {
		t.Fatal("a handle without a backend type must be rejected")
	}
	if err := ValidateNamed("capabilities/statestore.input", []byte(`{"workspace":"web-prod"}`)); err != nil {
		t.Fatalf("a valid resolve input must validate: %v", err)
	}
	// An empty workspace must fail closed at the input Contract (guardian slice-3 Flag B): the core
	// validates the resolve INPUT before invoking, so a malformed/empty workspace fails in the core.
	if err := ValidateNamed("capabilities/statestore.input", []byte(`{"workspace":""}`)); err == nil {
		t.Fatal("an empty workspace must be rejected by the input Contract")
	}
}

// TestIpamContract is the co-fidelity guard for ADR-0111: the class-level capabilities/ipam.{input,output}
// Contracts accept a representative provider-agnostic allocation request + handle and reject malformed ones —
// especially the §2.4 pool/role mutual-exclusion in the input.
func TestIpamContract(t *testing.T) {
	// A valid request: allocate a /24 from a pool, scoped to a region, keyed for idempotency.
	if err := ValidateNamed("capabilities/ipam.input", []byte(`{"key":"app-subnet","pool":"10.30.0.0/16","size":24,"region":"eu-west"}`)); err != nil {
		t.Fatalf("a valid ipam request must validate: %v", err)
	}
	// A valid request via role instead of pool.
	if err := ValidateNamed("capabilities/ipam.input", []byte(`{"key":"db-subnet","role":"app-prod","size":26,"tenant":"eu-sovereign","vlanGroup":"prod"}`)); err != nil {
		t.Fatalf("a valid role-based ipam request must validate: %v", err)
	}
	// §2.4: pool AND role together is a schema violation (no implicit precedence).
	if err := ValidateNamed("capabilities/ipam.input", []byte(`{"key":"x","pool":"10.0.0.0/8","role":"app-prod","size":24}`)); err == nil {
		t.Fatal("pool and role together must be rejected (oneOf, §2.4)")
	}
	// Neither pool nor role is a violation (exactly one required).
	if err := ValidateNamed("capabilities/ipam.input", []byte(`{"key":"x","size":24}`)); err == nil {
		t.Fatal("a request with neither pool nor role must be rejected")
	}
	// Missing the required key (idempotency identity, F1).
	if err := ValidateNamed("capabilities/ipam.input", []byte(`{"pool":"10.0.0.0/8","size":24}`)); err == nil {
		t.Fatal("a request without key must be rejected")
	}
	// Missing the required size.
	if err := ValidateNamed("capabilities/ipam.input", []byte(`{"key":"x","pool":"10.0.0.0/8"}`)); err == nil {
		t.Fatal("a request without size must be rejected")
	}
	// A valid handle.
	if err := ValidateNamed("capabilities/ipam.output", []byte(`{"cidr":"10.30.4.0/24","vlanId":100,"gateway":"10.30.4.1"}`)); err != nil {
		t.Fatalf("a valid ipam handle must validate: %v", err)
	}
	// A handle without a cidr is malformed.
	if err := ValidateNamed("capabilities/ipam.output", []byte(`{"vlanId":100}`)); err == nil {
		t.Fatal("an ipam handle without a cidr must be rejected")
	}
	// A VLAN id out of range must fail closed at the class Contract.
	if err := ValidateNamed("capabilities/ipam.output", []byte(`{"cidr":"10.30.4.0/24","vlanId":9999}`)); err == nil {
		t.Fatal("an out-of-range vlanId must be rejected")
	}
}

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
	if len(all) != 110 {
		t.Fatalf("expected 110 embedded documents, got %d", len(all))
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
