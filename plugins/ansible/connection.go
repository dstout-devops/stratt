package ansible

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
func connectionVars(c *connectionParams, knownHosts string, readDir func(string) ([]string, error), stage func(string) (string, error)) (map[string]string, error) {
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
		dst := filepath.Join(dir, "connection_key")
		if err := os.WriteFile(dst, raw, 0o600); err != nil {
			return "", fmt.Errorf("stage connection key: %w", err)
		}
		return dst, nil
	}
}
