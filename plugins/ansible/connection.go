package ansible

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// connectionParams is the AUTHENTICATION half of reaching a target (ansible.input.v6,
// ADR-0126 D1). The ADDRESS half is never here: it is the mgmt.address Facet the core
// resolves into the typed Target coordinate (ADR-0084 D1/D2), which buildInventory
// renders. This is the split AWX draws between an inventory host and a machine
// credential, and it is the split ADR-0084 D4 described but nothing implemented — every
// Workflow hand-wrote `ansible_ssh_private_key_file: /tmp/<name>-key` in extraVars, so
// the credential the Step was AUTHORIZED to use and the file it actually read were two
// facts nothing kept in agreement (§2.4).
type connectionParams struct {
	User            string           `json:"user,omitempty"`
	CredentialRef   string           `json:"credentialRef,omitempty"`
	File            string           `json:"file,omitempty"`
	HostKeyChecking string           `json:"hostKeyChecking,omitempty"`
	Jump            []connectionAuth `json:"jump,omitempty"`
}

// connectionAuth is the per-hop auth for a reached-via chain (ADR-0126 D3). No address
// field, deliberately: a hop's address comes from its own Entity's mgmt.address, so
// there is no second copy here to drift out of sync with the graph's (§2.4).
type connectionAuth struct {
	User          string `json:"user,omitempty"`
	CredentialRef string `json:"credentialRef,omitempty"`
	File          string `json:"file,omitempty"`
}

// Host-key policies (ADR-0126 D2). The default is accept-new, never off: it accepts a
// first key — which a host provisioned minutes ago necessarily has none of — and
// REFUSES a changed one, which is the property worth having. `off` still exists,
// because some estates genuinely need it, but it now has to be written down.
const (
	HostKeyStrict    = "strict"
	HostKeyAcceptNew = "accept-new"
	HostKeyOff       = "off"
)

// connectionVars renders the ansible connection variables for the inventory. The SHIM
// authors every ansible_* key here; the core authors none (§1.4, ADR-0084 D3).
//
// readDir lists a credential mount's files, so the ref→path resolution is the same
// tested seam vaultPasswordFile uses rather than a second one. stage copies the resolved
// key to a private-mode file and returns its path — see the call site for why that copy
// is unavoidable. Both are injected so the rendering is unit-tested without a pod.
func connectionVars(c *connectionParams, hops []Hop, knownHosts string, readDir func(string) ([]string, error), stage func(string) (string, error)) (map[string]string, error) {
	vars := map[string]string{}
	if c == nil {
		c = &connectionParams{}
	}
	if c.User != "" {
		vars["ansible_user"] = c.User
	}
	if c.CredentialRef != "" {
		mounted, err := credentialFile("connection", c.CredentialRef, c.File, "params.connection.file", readDir)
		if err != nil {
			return nil, err
		}
		// The mount CANNOT be handed to ssh directly, and this is the reason the whole
		// mechanism was hand-rolled in plays rather than built here. dispatch projects
		// credential files at 0440 (dispatch.go) — group-readable on purpose, so the
		// non-root execution pod can read them at all under its fsGroup. ssh then
		// refuses the key outright: "Permissions 0440 are too open. It is required that
		// your private key files are NOT accessible by others."
		//
		// So the key is staged into the run directory at 0600. Every play that needed
		// SSH was already doing exactly this copy by hand, to /tmp, as a bootstrap task
		// — the shim doing it once is strictly better: it is bounded to the runner dir
		// that is already torn down with the Run, the mode is correct by construction
		// rather than per-author, and it stops being something each play remembers.
		path, err := stage(mounted)
		if err != nil {
			return nil, err
		}
		vars["ansible_ssh_private_key_file"] = path
	}

	args, err := sshCommonArgs(c, knownHosts, readDir)
	if err != nil {
		return nil, err
	}
	// The jump chain, rendered by the SHIM from the core-resolved coordinates.
	jspec, keyOpts, jerr := proxyJump(hops, c.Jump, stage, readDir)
	if jerr != nil {
		return nil, jerr
	}
	if jspec != "" {
		args = append(args, "-o", "ProxyJump="+jspec)
		args = append(args, keyOpts...)
	}
	if len(args) > 0 {
		vars["ansible_ssh_common_args"] = strings.Join(args, " ")
	}
	return vars, nil
}

// sshCommonArgs builds the -o options the connection needs: the host-key policy and,
// when the target sits behind a chain, the ProxyJump. Returned as a token slice and
// joined by the caller so each option is composed here rather than by string-appending
// at three call sites — the shape that let `-o StrictHostKeyChecking=no` get
// copy-pasted into three Workflows in the first place.
func sshCommonArgs(c *connectionParams, knownHosts string, readDir func(string) ([]string, error)) ([]string, error) {
	var args []string
	switch policy := c.HostKeyChecking; policy {
	case "", HostKeyAcceptNew:
		// The contract default. Worth only as much as the known-hosts file it writes
		// to, which is why one is always passed — `UserKnownHostsFile=/dev/null`, the
		// thing this replaces, made accept-new and off identical in effect.
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	case HostKeyStrict:
		args = append(args, "-o", "StrictHostKeyChecking=yes")
	case HostKeyOff:
		// Explicit, declared, and visible in Git review — which is the whole change.
		args = append(args, "-o", "StrictHostKeyChecking=no")
	default:
		return nil, fmt.Errorf("connection.hostKeyChecking %q is not one of %s, %s, %s",
			policy, HostKeyStrict, HostKeyAcceptNew, HostKeyOff)
	}
	if c.HostKeyChecking != HostKeyOff && knownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+knownHosts)
	}
	return args, nil
}

// proxyJump renders the core-resolved reached-via chain into ssh's ProxyJump (ADR-0126
// D3). The chain's TOPOLOGY comes from the graph — one hop per reached-via Relation,
// each carrying the coordinate from its OWN Entity's mgmt.address — and the per-hop
// AUTH comes from params.connection.jump, the same address-vs-credential split ADR-0084
// D4 drew for the target itself.
//
// ssh's -J takes hops nearest-first as [user@]host[:port], comma-separated, which is
// exactly the order the core resolves them in; nothing here re-orders, because a
// silently-reversed chain would connect through the wrong box and still look like it
// worked.
func proxyJump(hops []Hop, auth []connectionAuth, stage func(string) (string, error), readDir func(string) ([]string, error)) (string, []string, error) {
	if len(hops) == 0 {
		return "", nil, nil
	}
	var specs []string
	var keyOpts []string
	for i, h := range hops {
		if h.Address == "" {
			// Core refuses this at resolve time; the shim refuses it again rather than
			// emitting a spec that would silently drop the hop and connect direct.
			return "", nil, fmt.Errorf("reached-via hop %d (%s) has no address", i, h.Name)
		}
		a := hopAuth(auth, i)
		spec := h.Address
		if a.User != "" {
			spec = a.User + "@" + spec
		}
		if h.Port > 0 {
			spec = spec + ":" + strconv.Itoa(int(h.Port))
		}
		specs = append(specs, spec)

		if a.CredentialRef != "" {
			mounted, err := credentialFile("connection.jump", a.CredentialRef, a.File, "params.connection.jump[].file", readDir)
			if err != nil {
				return "", nil, err
			}
			// Same 0440-vs-ssh constraint as the target's own key: a bastion key is a
			// private key too, and ssh applies the same permission check to it.
			path, err := stage(mounted)
			if err != nil {
				return "", nil, err
			}
			// -o IdentityFile is additive in ssh and applies across the jump chain;
			// ssh tries each offered key. Per-hop key BINDING (which key for which hop)
			// needs a generated ssh_config, which is a bigger seam than one estate has
			// asked for — stated here rather than pretended.
			keyOpts = append(keyOpts, "-o", "IdentityFile="+path)
		}
	}
	return strings.Join(specs, ","), keyOpts, nil
}

// hopAuth picks the auth entry for hop i, reusing the LAST entry when the array is
// shorter — the common "same jump credential for every hop" case, declared once. An
// empty array means no per-hop auth at all, which is legitimate: an agent-forwarded or
// certificate-based bastion needs none.
func hopAuth(auth []connectionAuth, i int) connectionAuth {
	switch {
	case len(auth) == 0:
		return connectionAuth{}
	case i < len(auth):
		return auth[i]
	default:
		return auth[len(auth)-1]
	}
}

// renderInventory is buildInventory plus the connection's group vars. It is a separate
// function rather than a widened buildInventory so the target-rendering rules ADR-0084
// pinned keep their own tests untouched — the connection is a NEW seam layered over
// them, not a change to how an address becomes a host line.
//
// The vars land in [all:vars] rather than in extraVars because that is where ansible
// expects connection configuration, and because extraVars is the operator's namespace:
// writing shim-owned keys into it is what v6 now refuses (ADR-0126 D1).
func renderInventory(targets []Target, groupVars map[string]string) string {
	inv := buildInventory(targets)
	if len(groupVars) == 0 {
		return inv
	}
	var b strings.Builder
	b.WriteString(inv)
	b.WriteString("\n[all:vars]\n")
	// Sorted, so the same resolved connection always renders the SAME inventory — the
	// byte-stability buildInventory already guarantees for targets (§1.8: two Runs
	// have to be comparable during descent).
	keys := make([]string, 0, len(groupVars))
	for k := range groupVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + groupVars[k] + "\n")
	}
	return b.String()
}

// credentialFile resolves a CredentialRef to its in-pod path. Generalized from
// vaultPasswordFile so the connection credential resolves through the ALREADY-TESTED
// path rather than a parallel copy of it: `kind` and `setting` only shape the
// diagnosis, so both callers fail the same way and name their own knob (§1.8).
func credentialFile(kind, ref, file, setting string, readDir func(string) ([]string, error)) (string, error) {
	dir := filepath.Join(credentialsMount, ref)
	if file != "" {
		return filepath.Join(dir, file), nil
	}
	names, err := readDir(dir)
	if err != nil {
		return "", fmt.Errorf("%s credentialRef %q is not mounted at %s — is it on the Step's credentialRefs? (%w)", kind, ref, dir, err)
	}
	names = credentialKeys(names)
	switch len(names) {
	case 0:
		return "", fmt.Errorf("%s credentialRef %q injects no files at %s", kind, ref, dir)
	case 1:
		return filepath.Join(dir, names[0]), nil
	default:
		sort.Strings(names)
		return "", fmt.Errorf("%s credentialRef %q injects %d files (%s) — set %s to choose one", kind, ref, len(names), strings.Join(names, ", "), setting)
	}
}

// stageKeyIn returns a stage func writing the key into dir at 0600 — the production
// stager for connectionVars. The destination lives inside the runner directory, so it is
// created and destroyed with the Run and never outlives it.
func stageKeyIn(dir string) func(string) (string, error) {
	return func(mounted string) (string, error) {
		raw, err := os.ReadFile(mounted)
		if err != nil {
			return "", fmt.Errorf("connection key %s is not readable in the pod: %w", mounted, err)
		}
		dst := stagedPathFor(dir, mounted)
		if err := os.WriteFile(dst, raw, 0o600); err != nil {
			return "", fmt.Errorf("stage connection key: %w", err)
		}
		return dst, nil
	}
}

// jumpChainOf returns the reached-via chain shared by every target in this slice, and
// REFUSES a slice whose targets disagree.
//
// The connection vars are inventory GROUP vars ([all:vars]) — one ProxyJump for the
// whole run — so a slice mixing targets behind different bastions cannot be rendered
// correctly, and rendering one of the chains would silently route the others through
// the wrong box. Core groups by Site already (ADR-0032); grouping by chain is the
// natural extension and is what this failure asks for, rather than the shim guessing
// (§1.8, §2.4).
func jumpChainOf(targets []Target) ([]Hop, error) {
	if err := requireOneChain(targets); err != nil {
		// Returning an empty chain here instead would connect DIRECT to every target —
		// silently ignoring a declared bastion, which is the precise failure this whole
		// decision exists to prevent (§1.8).
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	return targets[0].Jump, nil
}

// chainKey is a comparable rendering of a chain, for the agreement check only.
func chainKey(hops []Hop) string {
	parts := make([]string, 0, len(hops))
	for _, h := range hops {
		parts = append(parts, h.Address+":"+strconv.Itoa(int(h.Port)))
	}
	return strings.Join(parts, "|")
}

// requireOneChain reports the disagreement jumpChainOf detects, as an error the caller
// surfaces — separated so the check is testable and the message names both offenders.
func requireOneChain(targets []Target) error {
	if len(targets) < 2 {
		return nil
	}
	first := chainKey(targets[0].Jump)
	for _, t := range targets[1:] {
		if k := chainKey(t.Jump); k != first {
			return fmt.Errorf("targets %s and %s are reached through different bastion chains (%q vs %q) — one Run renders ONE ProxyJump, so split them into separate Steps or Views rather than have the shim pick", targets[0].Name, t.Name, first, k)
		}
	}
	return nil
}

// stagedPathFor names a staged key from the mount it came from. A FIXED name here made
// the target key and every hop key overwrite one another, so the last one written
// silently became the key for all of them — the destination is computed separately so
// that collision is directly testable.
func stagedPathFor(dir, mounted string) string {
	rel := strings.TrimPrefix(mounted, credentialsMount+"/")
	return filepath.Join(dir, "key_"+strings.NewReplacer("/", "_", ".", "_").Replace(rel))
}

// credentialKeys drops Kubernetes' atomic-writer internals from a credential mount
// listing. A projected Secret volume is NOT a flat directory: it holds a timestamped
// data directory and a `..data` symlink pointing at it, alongside one symlink per key.
//
//	/runner/credentials/app-node-ssh/
//	  ..2026_07_25_23_34_34.3778146225/   ← the real data dir
//	  ..data -> ..2026_07_25_23_34_34.…   ← the atomic-swap pointer
//	  id_ed25519 -> ..data/id_ed25519     ← the only entry that is a KEY
//
// Every internal entry begins with "..", which the kubelet guarantees precisely so a
// consumer can filter them; a Secret data key cannot start with "." at all (K8s validates
// keys against [-._a-zA-Z0-9]+ with no leading dot for projected volumes).
//
// Without this, a single-key ref lists as THREE entries and the "which file did you mean"
// diagnosis fires on a ref that is completely unambiguous. Found by the app-cert demo, on
// a real mount; no unit test could have found it, because the fake listers all return the
// flat directory a mount is imagined to be. vaultPasswordFile has carried the same bug
// since ADR-0117 — invisible because vault declarations set `file:` and never take the
// inference path — and is fixed by sharing this function.
func credentialKeys(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if strings.HasPrefix(n, "..") {
			continue
		}
		out = append(out, n)
	}
	return out
}
