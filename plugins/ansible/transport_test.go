package ansible

import (
	"errors"
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
			TransportCoordinates: []byte(doc)}}, eeManifest("kubernetes.core", "community.vmware", "amazon.aws"), anyBinary)
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
		TransportCoordinates: []byte(`{"kind":"winrm"}`)}}, eeManifest(), anyBinary)
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
		eeManifest("community.general"), anyBinary)
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
		eeManifest("kubernetes.core"), noBinary)
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
		eeManifest("community.vmware"), noBinary); err != nil {
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
	}, exploded, noBinary); err != nil {
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

// Coordinates the core validated but the shim cannot read means the two disagree about the
// pinned schema — schema drift, which §1.5 makes blocking rather than something to degrade past.
func TestUnreadableCoordinatesFailRatherThanDegrade(t *testing.T) {
	err := validateTransports([]Target{{Name: "web-01", TransportKind: "kubectl",
		TransportCoordinates: []byte(`{not json`)}}, eeManifest("kubernetes.core"), anyBinary)
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
	err := validateTransports(mixed, eeManifest("kubernetes.core"), anyBinary)
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
	if err := validateTransports(mixed, eeManifest("kubernetes.core", "community.vmware"), anyBinary); err != nil {
		t.Fatalf("an image carrying both must serve both: %v", err)
	}
}
