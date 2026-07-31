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

// TestStorageDatastoreContract is the ADR-0115 co-fidelity guard: the pinned storage.datastore Facet
// (the one schema read breadth ships, WITH its consuming datastores View, §1.1) accepts the vcenter
// plugin's real emission and rejects drift (closed).
func TestStorageDatastoreContract(t *testing.T) {
	ok := []byte(`{"name":"ds-vmfs-01","type":"VMFS","capacity":1099511627776,"freeSpace":549755813888}`)
	if covered, err := ValidateFacet("storage.datastore", ok); !covered || err != nil {
		t.Fatalf("a valid datastore facet must validate: covered=%v err=%v", covered, err)
	}
	if covered, err := ValidateFacet("storage.datastore", []byte(`{"type":"VMFS","undeclared":true}`)); !covered || err == nil {
		t.Fatalf("storage.datastore must reject undeclared keys (closed): covered=%v err=%v", covered, err)
	}
	if covered, err := ValidateFacet("storage.datastore", []byte(`{"capacity":"lots"}`)); !covered || err == nil {
		t.Fatalf("storage.datastore must reject a non-integer capacity: covered=%v err=%v", covered, err)
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
	// vSphere's emission (plugins/vcenter/normalize.go, ADR-0115 F1): {name} ONLY — a portgroup has no
	// cidr; its moref is the identity key and source is a label, so the shared union carries just the
	// declared name. This is the third Source; it was the latent write-path break charter-guardian caught.
	if covered, err := ValidateFacet("net.subnet", []byte(`{"name":"dc1-web-vlan100"}`)); !covered || err != nil {
		t.Fatalf("vSphere net.subnet emission must validate: covered=%v err=%v", covered, err)
	}
	// The pre-fix vSphere shape (with the provider-local moref/kind/source keys) MUST be rejected — proof
	// the closed union would have broken vSphere's write path, and that F1 removed exactly those keys.
	if covered, err := ValidateFacet("net.subnet", []byte(`{"name":"pg","moref":"dvportgroup-1","kind":"DistributedVirtualPortgroup","source":"vsphere"}`)); !covered || err == nil {
		t.Fatalf("the pre-F1 vSphere shape (moref/kind/source) must be rejected by the closed union: covered=%v err=%v", covered, err)
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
	// +ansible.template and +ansible.credential (ADR-0128): the template projection deepened
	// and gained the pinned schema it never had, and the credential mirror that makes
	// "which templates use this credential" a graph traversal.
	// +ansible.workflow (ADR-0129): the workflow mirror gained nodeCount/hasApprovalGate
	// and the pinned schema its write seam never had.
	// +ansible.user (ADR-0130): AWX's local ACCOUNT table — deliberately not identity.subject,
	// which has a single write-owner, and never read by authz (ADR-0079 INV-3).
	// +ansible.label (ADR-0132): an AWX label is an Entity, because a plugin's label KEYS
	// are a static grant allowlist and an AWX label name is only known at read time.
	// +ansible.executionenvironment (ADR-0133): the image an AWX job template runs in, as a
	// SUPPLY-CHAIN fact. AWX instance groups are deliberately not projected (D4).
	// +ansible.input.v7 (ADR-0134): `playbook`, a path within the project tree the Step's
	// Actuator declares via contentDir. A SIBLING of v6 rather than a widening of it —
	// an Actuator input Contract is a wire promise to Step authors (ADR-0132 D4).
	// 152 → 69: ADR-0138 D3/D4 moved 83 SELF-contract files out of the binary and into the
	// plugin trees that own them. What remains embedded is the SEAM set — capabilities/, facets/,
	// intents/, outputs/, policy/ — plus the self contracts of NEUTRALLY-NAMED surfaces that no
	// single vendor owns: `cert-issuer` (§1.5 says explicitly that a step-ca plugin could
	// implement it), `adopt`, and the retired `webhook`. A neutral name means more than one plugin
	// may implement it, which makes the document a seam by D3's own definition.
	//
	// Worth recording because the ADR's census was wrong: it read "22 of 152" by counting 13
	// actuator FILES plus 9 action DIRECTORIES, but those directories hold 78 files. The real
	// self-contract set was 91 documents, ~4× what the decision was sized against.
	// 69 → 70: +intents/dnsrecord.v2 (ADR-0144 D6). The ADR-0110 straggler — every other
	// named singleton got a v2 carrying `requires: [provisioning]` and dnsrecord did not,
	// which left the kind not merely unused but UNDECLARABLE (v1 REQUIRES the removed
	// builder/buildWorkflow seam and closes with additionalProperties: false). An Intent
	// SPEC schema is a seam by D3's definition, so it stays embedded here rather than
	// moving into the dns plugin's tree: the kind belongs to the estate, not the provider.
	// 70 → 71: +facets/app.deliverable (ADR-0148 follow-up b). The CHART delivery form needed a
	// facet an observe expectation could actually READ, and the software.* facets cannot be one:
	// software.chart is a `charts` component LIST so the form-agnostic advisory pass can walk it
	// (ADR-0080), while facetAtPath walks maps only and `contains` matches a whole element by
	// DeepEqual — so asking "is the deployed chart at version X" would have meant enumerating
	// every field the Normalizer happened to set, including appVersion, a fact about the chart
	// rather than desired state (ADR-0148 D3). It is the split the PACKAGE form already shipped
	// (app.config scalar + software.package list), arrived at from the other side. A SEAM by
	// ADR-0138 D3's definition: a Facet schema belongs to the estate, not to the collector that
	// happens to write it today.
	// 71 → 72: +intents/application.v3 (ADR-0148 follow-up b), and the reason is a finding rather
	// than a bump. `chartVersion` was first ADDED TO v2 IN PLACE, on the argument that a new typed
	// property is not the "tightening" that file's header warns about — nothing in the repo used
	// the key, so nothing that parsed stopped parsing. This test agreed, because it re-derives
	// every hash from the shipped files and a self-consistent edit is invisible to it. A store
	// holding the previous pin row did not: strattd refused to start with `contract drift:
	// intents/application v2 is pinned to 02c02872… but the shipped document hashes to d342b698…`.
	// The pin is over the DOCUMENT, not over anyone's judgement about whether the change was
	// compatible — which is precisely what a hash exists to stop being load-bearing (§1.5).
	// 72 → 75: +actions/cert-issuer/sign.{input,output} and +outputs/csr (CERT-2). All three are
	// SEAMS by ADR-0138 D3's definition rather than plugin-owned documents. `cert-issuer` is a
	// NEUTRALLY-NAMED surface — §1.5 says explicitly that a step-ca plugin could implement it — so
	// its Action contracts stay embedded, and `outputs/csr` is the shape an Actuator pins to hand a
	// value to a later Step, which makes it a promise to Step AUTHORS rather than to one plugin.
	//
	// outputs/csr carries exactly one field, closed, and that is the design rather than economy:
	// the private key is born on the target and never crosses the wire (§2.5, ADR-0050), so a
	// schema admitting anything beside the CSR would be an invitation to send more.
	// 77 since AWX-009 added facets/ansible.notification — where an AWX Controller sends job
	// outcomes, projected as name + driver + config KEY NAMES only, because AWX returns
	// non-secret configuration in the clear and for the commonest driver the cleartext field IS
	// the credential (§2.5). 76 was ADR-0150 D5's facets/cert.presented — what a HOST actually
	// presents, read back from the delivered file, beside the CLM Syncer's cert.expiry which says
	// what was ISSUED. The count is deliberate: a new pinned document is an act, not a side
	// effect (§1.5).
	if len(all) != 77 {
		t.Fatalf("expected 77 embedded documents, got %d — the shipped set is the SEAM set now "+
			"(ADR-0138 D3/D4); a plugin's own contracts live in plugins/<n>/contracts/", len(all))
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
	// ansible.input v8 (the connection type + the credential forms, ADR-0153) resolves as the
	// current version; v1–v7 stay pinned alongside it (every version keeps its own pin row — only the
	// LOOKUP collapses to the highest). It is now ESTATE-resident (ADR-0138 D3/D4), so the
	// version-sibling rule is asserted where the document actually lives — and asserting it there
	// is what proves RegisterEstate reproduces load()'s semantics rather than inventing its own.
	estateVersions := map[string]int{}
	for _, c := range EstateContracts() {
		if c.Version > estateVersions[c.Name] {
			estateVersions[c.Name] = c.Version
		}
	}
	if estateVersions["actuators/ansible.input"] != 8 {
		t.Fatalf("ansible.input current version: %d (estate-resident since ADR-0138 D4)",
			estateVersions["actuators/ansible.input"])
	}
	// intents/application v2 types `port` (ADR-0118 follow-up); v3 adds `chartVersion` for the chart
	// delivery form (ADR-0148 follow-up b). Both are SIBLING versions rather than edits, and the two
	// have different reasons worth keeping apart. v2 existed because tightening a type is BREAKING:
	// `port: 443` parsed under v1 and does not under v2. v3 exists because A PIN IS OVER THE
	// DOCUMENT — the v3 change is additive and compatible by any reading, and mutating v2 in place
	// still stopped strattd dead with `contract drift: … is pinned to 02c02872… but the shipped
	// document hashes to d342b698…`. Compatibility is not the question a pin asks (§1.5). Older
	// versions keep their pin rows; only the lookup moves.
	if versions["intents/application"] != 3 {
		t.Fatalf("intents/application current version: %d", versions["intents/application"])
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
