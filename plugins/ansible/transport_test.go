package ansible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func noBinary(string) (string, error)    { return "", errors.New("not found") }
func anyBinary(n string) (string, error) { return "/usr/local/bin/" + n, nil }

// THE PROPERTY THIS WHOLE DECISION EXISTS FOR: one View, one Run, three substrates, and the
// inventory names each host's own transport. Rendered as GROUP vars this is impossible —
// there is one value for the whole Run — so a mixed-substrate View could not be converged at
// all, and every converge Workflow would have to name a substrate (the thing ADR-0151
// removed from every declaration above the provider).
func TestOneRunConvergesThreeSubstrates(t *testing.T) {
	inv := buildInventory([]Target{
		{Name: "web-01", TransportKind: "kubectl",
			TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"stratt-hosts","pod":"web-01"}`)},
		{Name: "web-02", TransportKind: "vmware_tools",
			TransportCoordinates: []byte(`{"kind":"vmware_tools","vmPath":"/DC0/vm/web-02"}`)},
		{Name: "web-03", TransportKind: "aws_ssm",
			TransportCoordinates: []byte(`{"kind":"aws_ssm","instanceId":"i-0abc","region":"eu-west-1"}`)},
		// A plain SSH host with no observed transport: ansible's own default, unchanged.
		{Name: "web-04", Address: "10.0.1.9"},
	})
	// SORTED, like every other var this inventory renders: the same resolved target set must
	// produce the SAME bytes, because that is what makes two Runs comparable during descent
	// (§1.8). It is why the aws_ssm keys precede ansible_connection here.
	for _, want := range []string{
		"web-01 ansible_connection=kubernetes.core.kubectl ansible_kubectl_namespace=stratt-hosts ansible_kubectl_pod=web-01",
		"web-02 ansible_connection=community.vmware.vmware_tools ansible_vmware_guest_path=/DC0/vm/web-02",
		"web-03 ansible_aws_ssm_instance_id=i-0abc ansible_aws_ssm_region=eu-west-1 ansible_connection=amazon.aws.aws_ssm",
		"web-04 ansible_host=10.0.1.9",
	} {
		if !strings.Contains(inv, want) {
			t.Errorf("missing host line:\n  want %q\n  got:\n%s", want, inv)
		}
	}
	// No transport reached [all:vars] — that is the group-var trap this design avoids.
	if strings.Contains(inv, "[all:vars]") {
		t.Errorf("transports must be HOST vars, never group vars:\n%s", inv)
	}
}

// An OBSERVED ssh transport is a statement (a Syncer determined this host is SSH-reachable),
// and it renders NO var — because ssh is ansible's own default and authoring
// `ansible_connection=ssh` would add a key that changes nothing.
func TestObservedSSHRendersNothing(t *testing.T) {
	inv := buildInventory([]Target{{Name: "h", Address: "10.0.0.1", TransportKind: "ssh",
		TransportCoordinates: []byte(`{"kind":"ssh"}`)}})
	if strings.Contains(inv, "ansible_connection") {
		t.Errorf("observed ssh must author no connection var: %s", inv)
	}
	if !strings.Contains(inv, "ansible_host=10.0.0.1") {
		t.Errorf("…and the address must still render: %s", inv)
	}
}

// A transport missing its coordinates fails the RUN rather than rendering a half-connection.
func TestIncompleteCoordinatesFailTheRun(t *testing.T) {
	cases := map[string]string{
		"kubectl without a namespace": `{"kind":"kubectl","pod":"web-01"}`,
		"vmware_tools without a vm":   `{"kind":"vmware_tools"}`,
		"aws_ssm without a region":    `{"kind":"aws_ssm","instanceId":"i-0abc"}`,
	}
	for name, doc := range cases {
		kind := strings.SplitN(doc[9:], `"`, 2)[0]
		err := validateTransports([]Target{{Name: "h", TransportKind: kind,
			TransportCoordinates: []byte(doc)}}, nil, eeManifest("kubernetes.core", "community.vmware", "amazon.aws"), anyBinary)
		if err == nil {
			t.Errorf("%s must fail the run", name)
		} else if !strings.Contains(err.Error(), "h") {
			t.Errorf("%s: the diagnosis must name the target: %v", name, err)
		}
	}
}

// A transport this shim does not implement must FAIL, not silently render nothing — falling
// back to ssh against a pod with no sshd is a connection failure blamed on the target rather
// than on the projection that named a method nobody implements (§1.8).
func TestUnknownTransportFailsRatherThanFallingBackToSSH(t *testing.T) {
	err := validateTransports([]Target{{Name: "web-01", TransportKind: "winrm",
		TransportCoordinates: []byte(`{"kind":"winrm"}`)}}, nil, eeManifest(), anyBinary)
	if err == nil {
		t.Fatal("an unimplemented transport must fail the run")
	}
	for _, want := range []string{"winrm", "web-01", "ssh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnosis must name the transport, the target, and what it would have "+
				"silently done instead; missing %q: %v", want, err)
		}
	}
}

// ── D6 · the EE tooling gate, on BOTH axes ───────────────────────────────────────────────

// The collection axis, reusing ADR-0153 D7's manifest read.
func TestTransportNeedingAnAbsentCollectionIsRefused(t *testing.T) {
	err := validateTransports([]Target{{Name: "web-01", TransportKind: "kubectl",
		TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"n","pod":"p"}`)}},
		nil, eeManifest("community.general"), anyBinary)
	if err == nil {
		t.Fatal("an ordinary EE ships neither kubernetes.core nor kubectl")
	}
	for _, want := range []string{"kubernetes.core", "kubectl", "community.general", "ADR-0117"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in the diagnosis: %v", want, err)
		}
	}
}

// THE SECOND AXIS, and it is the one ADR-0153 D7 did not have: the collection can be present
// and the transport still unusable, because the connection plugin EXECS a binary. A gate that
// only checked collections would pass this and fail at connect time.
func TestCollectionPresentButBinaryMissingIsStillRefused(t *testing.T) {
	err := validateTransports([]Target{{Name: "web-01", TransportKind: "kubectl",
		TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"n","pod":"p"}`)}},
		nil, eeManifest("kubernetes.core"), noBinary)
	if err == nil {
		t.Fatal("kubernetes.core alone does not make kubectl work — the plugin execs the binary")
	}
	if !strings.Contains(err.Error(), "kubectl") || !strings.Contains(err.Error(), "PATH") {
		t.Errorf("the diagnosis must name the binary and where it was looked for: %v", err)
	}
}

// vmware_tools needs a collection and NO binary — a Python library, not an exec. Demanding one
// would refuse a perfectly usable image.
func TestVMwareToolsNeedsNoBinary(t *testing.T) {
	if err := validateTransports([]Target{{Name: "vm", TransportKind: "vmware_tools",
		TransportCoordinates: []byte(`{"kind":"vmware_tools","vmPath":"/DC0/vm/x"}`)}},
		nil, eeManifest("community.vmware"), noBinary); err != nil {
		t.Fatalf("vmware_tools is a python library, not an exec: %v", err)
	}
}

// ssh and no-transport ask nothing of the image. A tooling failure on the path every existing
// estate uses would be a self-inflicted outage.
func TestSSHAndAbsentTransportNeverConsultTheImage(t *testing.T) {
	exploded := func(string) ([]byte, error) {
		t.Helper()
		t.Fatal("an ssh-only run must not read the content manifest")
		return nil, nil
	}
	if err := validateTransports([]Target{
		{Name: "a", Address: "10.0.0.1"},
		{Name: "b", Address: "10.0.0.2", TransportKind: "ssh", TransportCoordinates: []byte(`{"kind":"ssh"}`)},
	}, nil, exploded, noBinary); err != nil {
		t.Fatalf("ssh needs nothing from the image: %v", err)
	}
}

// ── D5 · two homes for one fact are refused, never resolved ──────────────────────────────

func TestStepDeclaredTypeAndObservedTransportAreRefusedTogether(t *testing.T) {
	err := refuseTransportAndDeclaredType(
		&connectionParams{Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios"},
		[]Target{{Name: "web-01", TransportKind: "kubectl"}})
	if err == nil {
		t.Fatal("a Step-declared type over an observed transport must be refused, not resolved — " +
			"picking a winner is the implicit precedence §2.4 refuses")
	}
	for _, want := range []string{"network_cli", "web-01", "kubectl", "§2.4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name both sides; missing %q: %v", want, err)
		}
	}
}

// The legitimate cases must stay legitimate: a NETWORK DEVICE is discovered rather than built,
// so nothing observes a transport for it and the Step declaring one is correct.
func TestNetworkDeviceWithNoObservedTransportIsFine(t *testing.T) {
	if err := refuseTransportAndDeclaredType(
		&connectionParams{Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios"},
		[]Target{{Name: "switch-01", Address: "10.0.0.1"}}); err != nil {
		t.Fatalf("nothing provisions a switch, so no Syncer observes its transport: %v", err)
	}
	// And an observed transport with NO Step-declared type is the ordinary built-host case.
	if err := refuseTransportAndDeclaredType(nil, []Target{{Name: "web-01", TransportKind: "kubectl"}}); err != nil {
		t.Fatalf("an observed transport alone is the normal case: %v", err)
	}
	// An explicit ssh on the Step is not a substrate claim and must not collide.
	if err := refuseTransportAndDeclaredType(&connectionParams{Type: ConnSSH, User: "root"},
		[]Target{{Name: "web-01", TransportKind: "ssh"}}); err != nil {
		t.Fatalf("ssh on both sides agrees: %v", err)
	}
}

// withDeclaredSSH adds ADR-0158 D2's declared reach method to a params document — the migration
// every Run against a host NOTHING OBSERVED now owes, applied to this package's own fixtures.
//
// DECLARED, not observed, and the distinction is load-bearing for a test corpus. No shipped
// provider writes `mgmt.transport: ssh`: kubecompute observes kubectl, vcenter observes
// vmware_tools, and awsec2 deliberately writes NOTHING (ADR-0142 D4 — KeyName says a key is
// authorized, not that sshd listens). A fixture carrying an observed ssh transport would model a
// shape production never produces; a fixture DECLARING ssh models exactly what these estates must
// now carry, which is the thing worth testing against.
func withDeclaredSSH(t *testing.T, params string) json.RawMessage {
	t.Helper()
	m := map[string]any{}
	if params != "" {
		if err := json.Unmarshal([]byte(params), &m); err != nil {
			t.Fatalf("withDeclaredSSH: fixture params are not a JSON object (%q): %v", params, err)
		}
	}
	if _, ok := m["connection"]; ok {
		t.Fatalf("withDeclaredSSH: %q already declares a connection — merging would hide which "+
			"one the test meant", params)
	}
	m["connection"] = map[string]any{"type": "ssh"}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("withDeclaredSSH: %v", err)
	}
	return raw
}

// ── ADR-0158 D1/D3 · silence is not ssh ──────────────────────────────────────────────────
//
// THE PROPERTY: absence of mgmt.transport is overloaded across shipped providers — awsec2
// withholds it deliberately (ssh is correct) and vcenter loses it when Tools stop (ssh is
// wrong) — so the shim cannot read absence as an answer. It refuses and makes somebody say.

// FALSIFICATION: delete the `requireReachMethod` call in shim.go, or the default arm of its
// switch, and this fails. Nothing else in the package asserts it.
func TestUnobservedAndUndeclaredTargetIsRefused(t *testing.T) {
	err := requireReachMethod([]Target{{Name: "ec2-01", Address: "10.0.0.7"}}, nil)
	if err == nil {
		t.Fatal("a target nobody observed and nobody declared must be REFUSED, not rendered as " +
			"ssh — inferring a reach method from silence is the shim asserting a fact no Syncer stated")
	}
	// D3 demands the target AND both remedies, because which one is right is not knowable from
	// here: the message has to let the operator decide, not pick for them.
	for _, want := range []string{"ec2-01", "connection.type: ssh", "mgmt.transport", "KeyName", "Tools"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the target and BOTH remedies; missing %q: %v", want, err)
		}
	}
}

// A nil connection block is the same case as an empty type and must refuse identically — five
// reference-estate Steps carry no connection block at all, so this is the common shape, not an edge.
func TestAbsentConnectionBlockRefusesJustTheSame(t *testing.T) {
	targets := []Target{{Name: "web-01", Address: "10.0.0.1"}}
	if requireReachMethod(targets, nil) == nil {
		t.Error("no connection block at all: refused")
	}
	if requireReachMethod(targets, &connectionParams{User: "root"}) == nil {
		t.Error("a connection block that sets a user but no type says nothing about the reach method")
	}
}

// The three ways a reach method IS stated, none of which may refuse. Each is a different half of
// the arc: observed by a Syncer, declared on the Step (D2), or declared through mgmt.address.
func TestAStatedReachMethodIsNeverRefused(t *testing.T) {
	cases := map[string]struct {
		targets []Target
		conn    *connectionParams
	}{
		"observed transport": {
			[]Target{{Name: "web-01", TransportKind: "kubectl"}}, nil},
		"declared ssh — D2's opt-in for a host nothing provisioned": {
			[]Target{{Name: "ec2-01", Address: "10.0.0.7"}}, &connectionParams{Type: ConnSSH}},
		"declared network_cli — ADR-0153's discovered device": {
			[]Target{{Name: "switch-01", Address: "10.0.0.1"}},
			&connectionParams{Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios"}},
		"a local target declares itself through mgmt.address (ADR-0153 D6)": {
			[]Target{{Name: "control", Address: "local"}}, nil},
	}
	for name, c := range cases {
		if err := requireReachMethod(c.targets, c.conn); err != nil {
			t.Errorf("%s: must not refuse: %v", name, err)
		}
	}
}

// ONE unobserved host in an otherwise observed View is still a refusal, and the diagnosis names
// THAT host rather than the Run. This is the vcenter case exactly: a fleet converges fine until
// one guest's Tools stop, and the whole point is that the one guest is nameable.
func TestOneUnobservedHostAmongObservedOnesIsNamed(t *testing.T) {
	err := requireReachMethod([]Target{
		{Name: "vm-01", TransportKind: "vmware_tools"},
		{Name: "vm-02", Address: "10.0.0.2"}, // tools stopped: stale address, transport gone
		{Name: "vm-03", TransportKind: "vmware_tools"},
	}, nil)
	if err == nil {
		t.Fatal("one host with no reach method refuses the Run — converging the other two and " +
			"failing on the third has already changed two machines")
	}
	if !strings.Contains(err.Error(), "vm-02") {
		t.Errorf("the diagnosis must name the unreached host: %v", err)
	}
	for _, observed := range []string{"vm-01", "vm-03"} {
		if strings.Contains(err.Error(), observed) {
			t.Errorf("...and must NOT name the hosts that were observed fine (%s): %v", observed, err)
		}
	}
}

// A big View names a bounded number of hosts. A refusal that prints two hundred names is a
// refusal an operator scrolls past, and the COUNT is what distinguishes one stale guest from an
// estate that never declared its type.
func TestManyUnreachedTargetsAreSummarisedNotEnumerated(t *testing.T) {
	targets := make([]Target, 0, 40)
	for i := range 40 {
		targets = append(targets, Target{Name: fmt.Sprintf("node-%02d", i), Address: "10.0.0.1"})
	}
	err := requireReachMethod(targets, nil)
	if err == nil {
		t.Fatal("forty unreached targets must still refuse")
	}
	if !strings.Contains(err.Error(), "40 targets") || !strings.Contains(err.Error(), "and 35 more") {
		t.Errorf("the count and the elision must both be present: %v", err)
	}
	if strings.Contains(err.Error(), "node-39") {
		t.Errorf("the list must be bounded, not the whole View: %v", err)
	}
}

// THE CALL SITE, not just the function. Every test above calls requireReachMethod directly, and
// every one of them would still pass if Run never invoked it — "a shipped check nothing calls"
// being the defect class this arc has already paid for. This one drives the whole shim, and it is
// the only test that fails if the call in shim.go is deleted.
func TestShim_UnobservedTargetRefusesBeforeAnsibleIsSpawned(t *testing.T) {
	req := Request{
		Params:  json.RawMessage(`{"play":"- hosts: all\n  tasks: []\n"}`),
		Targets: []Target{{Name: "ec2-01", Address: "10.0.0.7"}},
	}
	run := &captureRunner{rc: 0}
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, t.TempDir(), req, run, noClone(t)); err != nil {
		t.Fatalf("the refusal is an emitted TERMINAL, not a returned error: %v", err)
	}
	// A TERMINAL EVENT, and this assertion is the one the live run earned. Returning a bare error
	// exits the process, so the Run failed with a `diagnostic-output` warn line, NO `task-terminal`,
	// and its own `error` field NULL — an operator opening the failed Run saw an empty cause, which
	// is §1.8's exact prohibition. Verified against a real cluster before it was fixed.
	if !bytes.Contains(buf.Bytes(), []byte(`"terminal":true`)) {
		t.Fatalf("a refused Run must emit a terminal fatal, or its cause reaches no surface:\n%s", buf.String())
	}
	// BEFORE, not during: a refusal that arrives after ansible has already connected somewhere is
	// the failure D3 exists to prevent, not a slower version of the fix.
	if len(run.args) != 0 {
		t.Errorf("ansible-runner must never be spawned for a refused Run, args=%v", run.args)
	}
	// The target AND both remedies, in the message that actually reaches the operator.
	for _, want := range []string{"ec2-01", "connection.type: ssh", "mgmt.transport"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the terminal must carry %q: %s", want, buf.String())
		}
	}
}

// The refusal must name the target the way an OPERATOR can act on it. core's `observedName` falls
// back to the Entity UUID when a host carries no `*.name` label, and the app-cert demo's node
// carries none — so the first live refusal read `target d56e01a6-…`, which is unambiguous and
// unusable. The address is what someone recognises; both are printed.
func TestTheRefusalNamesTheAddressAndNotOnlyAUUID(t *testing.T) {
	err := requireReachMethod([]Target{
		{Name: "d56e01a6-7ad6-4cfb-b9a9-04362655a10e", Address: "app-node.stratt.svc.cluster.local"},
	}, nil)
	if err == nil {
		t.Fatal("still a refusal")
	}
	for _, want := range []string{"d56e01a6-7ad6-4cfb-b9a9-04362655a10e", "app-node.stratt.svc.cluster.local"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnosis must carry %q — the UUID is what every descent link keys on, "+
				"the address is what an operator searches for, and neither substitutes: %v", want, err)
		}
	}
	// No parenthetical when the address adds nothing, so an estate whose targets ARE named does not
	// get `web-01 (web-01)`.
	same := requireReachMethod([]Target{{Name: "web-01", Address: "web-01"}}, nil)
	if strings.Contains(same.Error(), "web-01 (web-01)") {
		t.Errorf("an address identical to the name must not be repeated: %v", same)
	}
	// And a target with no address at all is named plainly, not as `h ()`.
	none := requireReachMethod([]Target{{Name: "h"}}, nil)
	if !strings.Contains(none.Error(), "target h:") {
		t.Errorf("a target with no address is named plainly: %v", none)
	}
}

// EVERY reach refusal must emit a terminal, not just ADR-0158's. The port's own conformance suite
// grades a stream with no terminal TaskEvent as a SeverityError — "the core folds a stream that
// never terminated to FAILED; the Run fails with no stated cause" — so a check that `return`s an
// error refuses correctly and tells nobody. Confirmed live before it was fixed: the Run's `error`
// field was NULL and the reason survived only as a warn diagnostic.
//
// Table-driven over all four axes deliberately: the defect was that three of them behaved one way
// and one another, and a per-axis test would let the next one drift again.
func TestEveryReachRefusalEmitsATerminalAndNotABareError(t *testing.T) {
	cases := map[string]Request{
		"unknown coordinates (validateTransports)": {
			Params:  json.RawMessage(`{"play":"- hosts: all"}`),
			Targets: []Target{{Name: "web-01", TransportKind: "winrm", TransportCoordinates: []byte(`{"kind":"winrm"}`)}},
		},
		"declared type over an observed transport (D5)": {
			Params:  json.RawMessage(`{"play":"- hosts: all","connection":{"type":"network_cli","networkOS":"cisco.ios.ios"}}`),
			Targets: []Target{{Name: "web-01", TransportKind: "kubectl", TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"n","pod":"p"}`)}},
		},
		"no reach method at all (ADR-0158 D3)": {
			Params:  json.RawMessage(`{"play":"- hosts: all"}`),
			Targets: []Target{{Name: "ec2-01", Address: "10.0.0.7"}},
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			run := &captureRunner{rc: 0}
			var buf bytes.Buffer
			if err := Run(context.Background(), &buf, t.TempDir(), req, run, noClone(t)); err != nil {
				t.Fatalf("a refusal is an emitted terminal, not a returned error: %v", err)
			}
			if !bytes.Contains(buf.Bytes(), []byte(`"terminal":true`)) {
				t.Fatalf("no terminal emitted — the Run fails with no stated cause:\n%s", buf.String())
			}
			if len(run.args) != 0 {
				t.Errorf("ansible-runner must not be spawned for a refused Run, args=%v", run.args)
			}
		})
	}
}

// THE COST THE ADR PREDICTED AND THIS DISPROVES. ADR-0158's last consequence says a mixed View
// needs the declared type for its unobserved half, that the declared type is a GROUP var, and
// that rendering it per-host is therefore required — a shim change.
//
// It is not required, and the reason is structural rather than lucky. `ssh` is the ONLY type that
// can coexist with an observed transport (D5 refuses every other one outright), and
// connectionTypeVars renders NOTHING for ssh — ansible's default already is ssh, so there is no
// `ansible_connection` group var to be overridden by anything. The observed half keeps its host
// vars, the unobserved half falls through to the default, and the two never meet.
//
// If someone later makes ssh author a group var, this test fails — which is the point.
func TestMixedViewNeedsNoPerHostRenderingOfTheDeclaredType(t *testing.T) {
	targets := []Target{
		{Name: "pod-01", TransportKind: "kubectl",
			TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"stratt-hosts","pod":"pod-01"}`)},
		{Name: "ec2-01", Address: "10.0.0.7"}, // nothing observed it; the Step speaks for it
	}
	conn := &connectionParams{Type: ConnSSH, User: "root"}
	if err := requireReachMethod(targets, conn); err != nil {
		t.Fatalf("declared ssh covers the unobserved half: %v", err)
	}
	if err := refuseTransportAndDeclaredType(conn, targets); err != nil {
		t.Fatalf("...and ssh is not a substrate claim, so it does not collide with kubectl: %v", err)
	}
	groupVars, err := connectionTypeVars(conn, false, eeManifest())
	if err != nil {
		t.Fatalf("connectionTypeVars: %v", err)
	}
	if _, ok := groupVars["ansible_connection"]; ok {
		t.Fatalf("declared ssh must author NO group connection var — if it ever does, the mixed "+
			"View breaks and per-host rendering becomes real work: %v", groupVars)
	}
	inv := renderInventory(targets, groupVars)
	if !strings.Contains(inv, "pod-01 ansible_connection=kubernetes.core.kubectl") {
		t.Errorf("the observed half keeps its host vars:\n%s", inv)
	}
	if !strings.Contains(inv, "ec2-01 ansible_host=10.0.0.7\n") {
		t.Errorf("the declared half carries an address and no connection var:\n%s", inv)
	}
}

// Coordinates the core validated but the shim cannot read means the two disagree about the
// pinned schema — schema drift, which §1.5 makes blocking rather than something to degrade past.
func TestUnreadableCoordinatesFailRatherThanDegrade(t *testing.T) {
	err := validateTransports([]Target{{Name: "web-01", TransportKind: "kubectl",
		TransportCoordinates: []byte(`{not json`)}}, nil, eeManifest("kubernetes.core"), anyBinary)
	if err == nil || !strings.Contains(err.Error(), "web-01") {
		t.Fatalf("unreadable coordinates must fail and name the target, got %v", err)
	}
}

// THE CONSEQUENCE THAT IS NOT A FLAW, pinned so nobody rediscovers it in a demo run.
//
// The transport is per HOST; the EE IMAGE is per Actuator, and therefore per Run. So the image
// a mixed-substrate Run uses must carry the UNION of the transports its targets need — one
// image with kubernetes.core AND community.vmware AND amazon.aws, not three images.
//
// That is a real operational fact rather than a design defect: the gate names exactly which
// piece is missing BEFORE anything runs, which is the difference between "build a wider EE" and
// a connection error three hosts into a converge. Recorded here because it bit immediately —
// kubecompute began observing `kubectl`, and every existing converge against its hosts would
// have started failing on the default EE if the demos had not been repointed at the kube
// variant (STRATT_EE_IMAGE, ADR-0117 D3's global default).
func TestOneImageMustCarryEveryTransportItsRunUses(t *testing.T) {
	mixed := []Target{
		{Name: "pod", TransportKind: "kubectl", TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"n","pod":"p"}`)},
		{Name: "vm", TransportKind: "vmware_tools", TransportCoordinates: []byte(`{"kind":"vmware_tools","vmPath":"/DC0/vm/x"}`)},
	}
	// An image with only ONE of the two is refused, and the diagnosis names the one it lacks —
	// not the one it has.
	err := validateTransports(mixed, nil, eeManifest("kubernetes.core"), anyBinary)
	if err == nil {
		t.Fatal("an image carrying one transport cannot serve a Run that uses two")
	}
	if !strings.Contains(err.Error(), "community.vmware") {
		t.Errorf("the diagnosis must name the MISSING collection: %v", err)
	}
	if strings.Contains(err.Error(), "reached by kubectl") {
		t.Errorf("…and not blame the one that is present: %v", err)
	}
	// The union satisfies it.
	if err := validateTransports(mixed, kubeconfigDeclared(), eeManifest("kubernetes.core", "community.vmware"), anyBinary); err != nil {
		t.Fatalf("an image carrying both must serve both: %v", err)
	}
}

// ── D4 · the REACH CREDENTIAL, the third axis ────────────────────────────────────────────
//
// The axis that was missing, and its absence cost a full capstone run. The two content checks
// above both PASSED for kubectl — the EE had kubernetes.core and a kubectl binary — so nothing
// refused, and the Run died at connect time with
//
//	runner_on_unreachable: Failed to create temporary directory … you may have been able to
//	authenticate and did not have permissions on the target directory
//
// which names the GUEST's filesystem for a failure that was entirely on the control node: the
// execution pod holds no cluster identity (AutomountServiceAccountToken: false, by design), so
// the API server refused the exec. §1.8 forbids exactly that — the abstraction hid the diagnosis.

// kubeconfigDeclared is a Step that brokered a reach credential.
func kubeconfigDeclared() *connectionParams {
	return &connectionParams{KubeconfigRef: &fileCredentialRef{CredentialRef: "hosts-kubeconfig"}}
}

func TestKubectlWithoutABrokeredKubeconfigIsRefused(t *testing.T) {
	target := []Target{{Name: "web-01", TransportKind: "kubectl",
		TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"stratt-hosts","pod":"web-01"}`)}}

	// A fully-equipped image is NOT enough: this is a declaration gap, not an image gap, and
	// proving it against an image that satisfies both content axes is what makes that distinct.
	err := validateTransports(target, nil, eeManifest("kubernetes.core"), anyBinary)
	if err == nil {
		t.Fatal("a kubectl transport with no brokered kubeconfig must refuse BEFORE the run — " +
			"otherwise the API server denies the exec and ansible blames the target")
	}
	// The diagnosis has to carry the fix and the reason, because the failure it replaces was
	// actively misleading about which side of the connection was at fault.
	for _, want := range []string{"kubeconfigRef", "kubectl", "cluster identity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in the diagnosis: %v", want, err)
		}
	}

	// …and the same target with a credential passes.
	if err := validateTransports(target, kubeconfigDeclared(), eeManifest("kubernetes.core"), anyBinary); err != nil {
		t.Fatalf("a brokered kubeconfig is exactly what this transport needs: %v", err)
	}
}

// An EMPTY credentialRef is not a credential. Without this the guard passes on
// `kubeconfigRef: {credentialRef: ""}` — a declaration that looks deliberate and brokers nothing.
func TestEmptyKubeconfigRefIsNotACredential(t *testing.T) {
	if err := validateTransports([]Target{{Name: "web-01", TransportKind: "kubectl",
		TransportCoordinates: []byte(`{"kind":"kubectl","namespace":"n","pod":"p"}`)}},
		&connectionParams{KubeconfigRef: &fileCredentialRef{}}, eeManifest("kubernetes.core"), anyBinary); err == nil {
		t.Fatal("an empty credentialRef brokers nothing and must refuse")
	}
}

// The credential is demanded ONLY by the transport that needs it. ssh, vmware_tools and aws_ssm
// carry their own credentials by their own routes (ADR-0156 D4), and demanding a kubeconfig of
// them would refuse every converge that has ever worked.
func TestOnlyKubectlDemandsAKubeconfig(t *testing.T) {
	for _, tc := range []struct{ name, kind, coords, collection string }{
		{"ssh", "ssh", `{"kind":"ssh"}`, ""},
		{"vmware_tools", "vmware_tools", `{"kind":"vmware_tools","vmPath":"/DC0/vm/x"}`, "community.vmware"},
		{"aws_ssm", "aws_ssm", `{"kind":"aws_ssm","instanceId":"i-0abc","region":"us-east-1"}`, "amazon.aws"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols := []string{}
			if tc.collection != "" {
				cols = append(cols, tc.collection)
			}
			if err := validateTransports([]Target{{Name: "h", TransportKind: tc.kind,
				TransportCoordinates: []byte(tc.coords)}}, nil, eeManifest(cols...), anyBinary); err != nil {
				t.Fatalf("%s must not be made to declare a kubeconfig: %v", tc.name, err)
			}
		})
	}
}
