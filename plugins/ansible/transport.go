package ansible

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ── the observed transport: how a target is reached (ADR-0156) ───────────────────────────────────
//
// `mgmt.address` says WHERE a target is; `mgmt.transport` says BY WHAT MEANS. Both are observed by
// the Syncer that saw the host, and both cross the port as typed coordinates the SHIM renders into
// ansible keys — the core authors none (§1.4, ADR-0084 D3).
//
// ── WHY PER-HOST AND NOT PER-RUN ────────────────────────────────────────────────────────────────
// The connection block from params renders into `[all:vars]`, one value for the whole Run. That is
// correct for a Step-declared connection and fatal for an observed one: a View holding a pod, a
// vSphere VM and an EC2 instance would have to pick one transport for all three, so a mixed-substrate
// View could not be converged at all.
//
// Rendered per HOST, one Assignment converges all three in ONE Run and the Intent, the Blueprint and
// the Assignment name no substrate. That is the converge-side equivalent of what ADR-0151 did for
// builds, and it is the reason this fact lives on the target.
//
// ── WHAT EACH TRANSPORT NEEDS, AND WHY IT IS CHECKED BEFORE THE RUN ─────────────────────────────
// None of these ships in ansible-core, and two of them need a BINARY on the control node as well as
// a collection. ADR-0153 D7 already learned what happens when a Contract accepts a value the running
// image cannot honour: a declaration that passes review and dies at connect time naming a python
// module the estate never wrote. So both axes are checked here, before the play runs.

// transportSpec is the mgmt.transport Facet document as the shim reads it. Every field is a
// COORDINATE; there is no credential here and no field one could occupy — authenticating to the
// target is the Step's brokered CredentialRef (§2.5, ADR-0084 D4's split applied to the transport).
type transportSpec struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Pod        string `json:"pod,omitempty"`
	Container  string `json:"container,omitempty"`
	VMPath     string `json:"vmPath,omitempty"`
	VMUuid     string `json:"vmUuid,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`
	Region     string `json:"region,omitempty"`
}

// Transport kinds (ADR-0156 D1). `local` is deliberately absent: a control-node target declares
// itself through mgmt.address's reserved value, and a second home for that fact would be resolved by
// a precedence rule §2.4 refuses.
const (
	TransportSSH         = "ssh"
	TransportKubectl     = "kubectl"
	TransportVMwareTools = "vmware_tools"
	TransportAWSSSM      = "aws_ssm"
)

// transportNeeds is what the EE must carry for a transport to work at all: the collection that
// provides the connection plugin, and any binary the plugin shells out to on the control node.
var transportNeeds = map[string]struct {
	collection string
	binary     string
}{
	TransportKubectl:     {collection: "kubernetes.core", binary: "kubectl"},
	TransportVMwareTools: {collection: "community.vmware"},
	TransportAWSSSM:      {collection: "amazon.aws", binary: "session-manager-plugin"},
	// ssh is ansible-core and needs nothing.
}

// transportVars renders one target's observed transport into its HOST vars.
//
// An unknown kind returns an error rather than no vars: silently rendering nothing would fall back
// to ssh against a pod that has no sshd, which is a connection failure blamed on the target instead
// of on the projection that named a transport nobody implements (§1.8).
func transportVars(t *transportSpec) (map[string]string, error) {
	if t == nil || t.Kind == "" {
		return nil, nil
	}
	switch t.Kind {
	case TransportSSH:
		// An OBSERVED ssh transport is a statement, not a no-op: it says a Syncer determined
		// this host is SSH-reachable. It renders no var because ssh is ansible's own default
		// and authoring `ansible_connection=ssh` would add a key that changes nothing.
		return nil, nil

	case TransportKubectl:
		if t.Namespace == "" || t.Pod == "" {
			return nil, fmt.Errorf("transport kubectl requires namespace and pod")
		}
		vars := map[string]string{
			"ansible_connection":        "kubernetes.core.kubectl",
			"ansible_kubectl_namespace": t.Namespace,
			"ansible_kubectl_pod":       t.Pod,
		}
		if t.Container != "" {
			vars["ansible_kubectl_container"] = t.Container
		}
		return vars, nil

	case TransportVMwareTools:
		// The VM is addressed by inventory PATH or by uuid; the plugin takes either.
		vars := map[string]string{"ansible_connection": "community.vmware.vmware_tools"}
		switch {
		case t.VMPath != "":
			vars["ansible_vmware_guest_path"] = t.VMPath
		case t.VMUuid != "":
			vars["ansible_vmware_guest_uuid"] = t.VMUuid
		default:
			return nil, fmt.Errorf("transport vmware_tools requires vmPath or vmUuid")
		}
		return vars, nil

	case TransportAWSSSM:
		if t.InstanceID == "" || t.Region == "" {
			return nil, fmt.Errorf("transport aws_ssm requires instanceId and region")
		}
		return map[string]string{
			"ansible_connection":          "amazon.aws.aws_ssm",
			"ansible_aws_ssm_instance_id": t.InstanceID,
			"ansible_aws_ssm_region":      t.Region,
		}, nil

	default:
		return nil, fmt.Errorf("observed transport %q is not one of %s, %s, %s, %s — the projection "+
			"named a connection method this shim does not implement, and falling back to ssh would "+
			"connect the wrong way and blame the target",
			t.Kind, TransportSSH, TransportKubectl, TransportVMwareTools, TransportAWSSSM)
	}
}

// requireTransportTooling refuses a transport whose collection or binary this EE lacks, BEFORE the
// play runs (ADR-0156 D6, extending ADR-0153 D7 with a second axis).
//
// The binary half is `exec.LookPath`, which — unlike `ansible-doc`, whose exit code D7 measured as
// useless for this — actually answers. The collection half reuses D7's read of the EE's own
// run-visible content manifest.
func requireTransportTooling(kinds map[string]bool, read func(string) ([]byte, error), look func(string) (string, error)) error {
	needed := make([]string, 0, len(kinds))
	for k := range kinds {
		if _, ok := transportNeeds[k]; ok {
			needed = append(needed, k)
		}
	}
	if len(needed) == 0 {
		return nil
	}
	sort.Strings(needed) // one deterministic diagnosis, not whichever map order won
	have, err := eeCollections(read)
	if err != nil {
		return fmt.Errorf("observed transports %s need collections, and this image's content "+
			"manifest could not be read to confirm them (%w) — an EE built outside our pipeline "+
			"publishes no manifest, so what it contains is unknown rather than adequate",
			strings.Join(needed, ", "), err)
	}
	for _, kind := range needed {
		need := transportNeeds[kind]
		if _, ok := have[need.collection]; !ok {
			return fmt.Errorf("a target is reached by %s, which needs the %s collection and this EE "+
				"does not ship it (installed: %s). Build an EE variant that includes it and select it "+
				"from the Actuator declaration (ADR-0117 D3) — the platform floor stays bounded to what "+
				"the platform's OWN content needs", kind, need.collection, describeCollections(have))
		}
		if need.binary == "" {
			continue
		}
		if _, err := look(need.binary); err != nil {
			return fmt.Errorf("a target is reached by %s, which shells out to %q on the control node "+
				"and this EE does not have it on PATH. The collection alone is not enough for this "+
				"transport — the connection plugin execs the binary", kind, need.binary)
		}
	}
	return nil
}

// requireTransportCredential refuses an observed transport whose REACH CREDENTIAL the Step never
// declared — the third axis, after the collection and the binary.
//
// FOUND BY RUNNING IT, and it cost a whole capstone run to find. The content checks above both
// passed: the EE carried kubernetes.core and a kubectl binary, so nothing refused, ansible connected
// as far as it could and the Run died with
//
//	runner_on_unreachable: Failed to create temporary directory. In some cases, you may have been
//	able to authenticate and did not have permissions on the target directory.
//
// Every word of which points at the TARGET's filesystem. The actual cause was the API server
// rejecting `kubectl exec` because the execution pod holds no credential at all — a control-node
// authorization failure, reported as a guest permissions problem. That is §1.8's exact prohibition:
// the abstraction hid the diagnosis, and an operator following that message would go and chmod a
// directory on a pod that was never the problem.
//
// A declaration-level check answers it before anything runs, and needs no filesystem: either the
// Step brokered a credential for this transport or it did not.
func requireTransportCredential(kinds map[string]bool, c *connectionParams) error {
	if !kinds[TransportKubectl] {
		return nil
	}
	if c != nil && c.KubeconfigRef != nil && c.KubeconfigRef.CredentialRef != "" {
		return nil
	}
	return fmt.Errorf("a target is reached by %s, and this Step brokers no kubeconfig for it — set "+
		"params.connection.kubeconfigRef.credentialRef. The GUEST needs nothing for kubectl (no sshd, "+
		"no account), but the CONTROL NODE does: the execution pod is spawned with no cluster identity "+
		"on purpose (AutomountServiceAccountToken: false), so `kubectl exec` has nothing to "+
		"authenticate with. Without this the Run does not refuse — it reaches the API server, is "+
		"denied, and ansible reports `unreachable: Failed to create temporary directory … did not "+
		"have permissions on the target directory`, which names the guest for a failure that is "+
		"entirely on this side (ADR-0156 D4)", TransportKubectl)
}

// osLookPath is the production binary probe for requireTransportTooling.
func osLookPath(name string) (string, error) { return exec.LookPath(name) }

// parseTransport decodes a target's transport coordinates. A document that does not parse is an
// error rather than "no transport": the core validated it against the pinned mgmt.transport schema
// before sending it, so unparseable here means the two disagree — which is a fact worth failing on
// (§1.5, schema drift is blocking) rather than degrading past.
func parseTransport(kind string, coordinates []byte) (*transportSpec, error) {
	if kind == "" {
		return nil, nil
	}
	spec := &transportSpec{Kind: kind}
	if len(coordinates) > 0 {
		if err := json.Unmarshal(coordinates, spec); err != nil {
			return nil, fmt.Errorf("transport coordinates for %q are unreadable: %w", kind, err)
		}
		// The port's `kind` is the legible one and wins: it is what core logged for descent.
		spec.Kind = kind
	}
	return spec, nil
}

// transportVarsFor is the Target-shaped wrapper buildInventory calls. It swallows nothing: the
// error is returned so the caller can refuse the run, and buildInventory — whose byte-stability is
// a §1.8 property other tests pin — renders no vars for a target whose transport is unusable, while
// validateTransports below is what actually fails the Run.
func transportVarsFor(t Target) (map[string]string, error) {
	spec, err := parseTransport(t.TransportKind, t.TransportCoordinates)
	if err != nil {
		return nil, err
	}
	return transportVars(spec)
}

// validateTransports checks every target's transport BEFORE anything runs: that it parses, that it
// renders, and that this EE carries the collection and binary it needs.
//
// One pass over the whole set rather than per-target lazily, because a Run that converges three
// hosts and then dies on the fourth's missing collection has already changed three machines. The
// tooling check is per DISTINCT kind, so a hundred pods ask about kubernetes.core once.
func validateTransports(targets []Target, c *connectionParams, read func(string) ([]byte, error), look func(string) (string, error)) error {
	kinds := map[string]bool{}
	for _, t := range targets {
		spec, err := parseTransport(t.TransportKind, t.TransportCoordinates)
		if err != nil {
			return fmt.Errorf("target %s: %w", t.Name, err)
		}
		if spec == nil {
			continue
		}
		if _, err := transportVars(spec); err != nil {
			return fmt.Errorf("target %s: %w", t.Name, err)
		}
		kinds[spec.Kind] = true
	}
	if err := requireTransportTooling(kinds, read, look); err != nil {
		return err
	}
	// The credential check comes LAST of the three deliberately: a missing collection is an image
	// problem and a missing kubeconfig is a declaration problem, and reporting the image one first
	// keeps the operator fixing things in the order they can actually fix them.
	return requireTransportCredential(kinds, c)
}

// refuseTransportAndDeclaredType is ADR-0156 D5: a Step-declared connection.type and an OBSERVED
// transport are refused together, never resolved.
//
// A host a PROVIDER BUILT is observed, so its transport is a Facet. A NETWORK DEVICE is discovered
// rather than built — nothing provisions a switch, so no Syncer observes a transport for it — and
// network_cli/netconf stay Step-declared, which is correct. When both apply to one target, picking
// a winner is the implicit precedence §2.4 refuses, and it is the same refusal ADR-0153 D6 already
// makes for a `local` target under a non-ssh type.
func refuseTransportAndDeclaredType(c *connectionParams, targets []Target) error {
	if c == nil || c.Type == "" || c.Type == ConnSSH {
		return nil
	}
	for _, t := range targets {
		if t.TransportKind == "" {
			continue
		}
		return fmt.Errorf("connection.type %s is declared on this Step, but target %s carries an "+
			"OBSERVED transport %q from its mgmt.transport Facet — two homes for one fact, and "+
			"picking between them is the precedence §2.4 refuses. A host a provider BUILT is "+
			"observed (its Syncer says how to reach it); a network device is DISCOVERED and declares "+
			"its type on the Step. Remove one",
			c.Type, t.Name, t.TransportKind)
	}
	return nil
}

// ── D1/D3 · a target nobody observed and nobody declared is REFUSED ──────────────────────────────

// requireReachMethod is ADR-0158 D1 and D3: a target with NO observed transport and NO declared
// connection.type has an UNKNOWN reach method, and the Run is refused rather than rendered as ssh.
//
// WHY ABSENCE CANNOT MEAN SSH — it is overloaded across shipped providers, and both readings are
// real today:
//
//   - `awsec2` withholds mgmt.transport DELIBERATELY and permanently: `KeyName` says a key is
//     AUTHORIZED, not that sshd is LISTENING, and ADR-0142 D4 forbids computing a reach fact. So
//     absence there means ssh, and ssh is correct.
//   - `vcenter` gates mgmt.transport on `ToolsRunningStatus`, while mgmt.address comes from guest
//     info vCenter CACHES. A VM whose tools stop keeps a stale address and LOSES its transport. So
//     absence there means the observation went away, and ssh is wrong.
//
// One value, two meanings, decided by which provider declined to write it. Rendering ssh for both is
// the shim asserting a fact about the host that no Syncer ever stated — the §1.2 line ADR-0142 D4
// drew for reach COORDINATES, applied to the reach METHOD. It is also §2.4's anti-GPO axiom in
// miniature: absence resolving to a default is precedence with nobody's name on it.
//
// REFUSE, DO NOT WARN. A warning on a converge that then attempts ssh anyway is strictly worse than
// either alternative — it runs the wrong thing and reports it quietly. What it replaces is ansible's
// own `unreachable`, which names the GUEST for a decision the control node made: the §1.8 failure
// this arc has now paid for four separate times.
func requireReachMethod(targets []Target, c *connectionParams) error {
	if c != nil && c.Type != "" {
		// SOMEBODY SAID, and which type they said does not matter here: `ssh` is D2's opt-in for a
		// host nothing observes, and a network type is ADR-0153's declared case. Both are a stated
		// intent, which is the whole property this check defends. A declared type that CONTRADICTS
		// an observed transport is refused separately and by name (D5, above) — never resolved here.
		return nil
	}
	var unreached []string
	for _, t := range targets {
		switch {
		case t.TransportKind != "":
			// Observed: a Syncer saw this host and said by what means.
		case t.Address == connLocal:
			// Declared — through mgmt.address's reserved value rather than either field above
			// (ADR-0153 D6), and buildInventory renders its `ansible_connection=local` host var.
			// This exemption is why the check reads the ADDRESS as well: the control node has no
			// transport to observe and needs no Step to speak for it.
		default:
			unreached = append(unreached, t.Name)
		}
	}
	if len(unreached) == 0 {
		return nil
	}
	return fmt.Errorf("%s: no observed mgmt.transport and no declared connection.type, so the reach "+
		"method is UNKNOWN — and rendering ssh for a host nobody observed would be this shim "+
		"asserting a fact about it (ADR-0158 D1, §1.2). There are two remedies and which one is "+
		"right depends on the host: (1) DECLARE IT — if these hosts are ssh-reachable, set "+
		"params.connection.type: ssh on this Step, which is where a host nothing provisioned states "+
		"how it is reached (an EC2 instance is permanently this case: KeyName means a key is "+
		"authorized, not that sshd listens); or (2) FIX THE OBSERVATION — if a provider should have "+
		"written mgmt.transport then its absence is the missing thing rather than the declaration "+
		"(a vSphere guest whose VMware Tools stopped loses its transport while keeping a CACHED "+
		"address, so it looks reachable and is not)", describeUnreached(unreached))
}

// describeUnreached names the refused targets in a BOUNDED way. A View of two hundred hosts must not
// turn one refusal into two hundred lines of log — and the count is the thing that tells an operator
// which of the two remedies they are looking at: one name is a stale guest, all of them is an estate
// that never declared its type.
func describeUnreached(names []string) string {
	const show = 5
	switch {
	case len(names) == 1:
		return "target " + names[0]
	case len(names) <= show:
		return fmt.Sprintf("%d targets (%s)", len(names), strings.Join(names, ", "))
	default:
		return fmt.Sprintf("%d targets (%s, and %d more)", len(names),
			strings.Join(names[:show], ", "), len(names)-show)
	}
}
