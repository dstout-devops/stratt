package ansible

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/sdk/pluginserve"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// commandRunner runs `ansible-runner` in dir, streaming each stdout line to onLine,
// and returns the process exit code. Injectable so the shim's interpretation is
// unit-tested without ansible-runner (which is never linked — subprocess only, §3).
type commandRunner interface {
	run(ctx context.Context, dir string, args []string, onLine func([]byte)) (rc int, err error)
}

type execRunner struct{ bin string }

func (e execRunner) run(ctx context.Context, dir string, args []string, onLine func([]byte)) (int, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	cmd.Stderr = cmd.Stdout // banners/tracebacks ride the same stream → surfaced as diagnostics (§1.8)
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 8<<20) // fact payloads are large
	for sc.Scan() {
		onLine(append([]byte(nil), sc.Bytes()...))
	}
	if werr := cmd.Wait(); werr != nil {
		if exitErr, ok := werr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil // non-zero is a RESULT (rc), not a spawn error
		}
		return -1, werr
	}
	return 0, nil
}

// params is the shim's read of the opaque desired — never the Contract (§1.5).
type params struct {
	Play      string         `json:"play"`
	ExtraVars map[string]any `json:"extraVars"`
	// SCM, when set, fetches the playbook from a git content-ref INSIDE the EE (the
	// git/GPL tooling stays a subprocess on this side of the port, never the core).
	SCM *scmParams `json:"scm,omitempty"`
	// Playbook, when set, names a play file inside project/ — which the CORE has already
	// mounted from the Actuator's declared contentDir (ADR-0134). The shim does not fetch
	// it and does not know where it came from: project/ arrives populated instead of being
	// cloned, so this reuses the branch SCM already has rather than adding a third one.
	Playbook string `json:"playbook,omitempty"`

	// ── The typed run knobs (ansible.input.v5, ADR-0117 D1) ──────────────────────
	// Each is a TYPED field, never a free-form flag string: the Contract bounds every
	// value (enums, patterns, ranges), and playbookFlags renders each as its own
	// token. That is what keeps this an argument surface rather than an injection one.
	Become    *becomeParams `json:"become,omitempty"`
	Limit     string        `json:"limit,omitempty"`
	Tags      []string      `json:"tags,omitempty"`
	SkipTags  []string      `json:"skipTags,omitempty"`
	Forks     int           `json:"forks,omitempty"`
	Diff      bool          `json:"diff,omitempty"`
	Verbosity int           `json:"verbosity,omitempty"`
	Timeout   int           `json:"timeout,omitempty"`
	// Vault is EITHER the v7 object (one identity) or a v8 array of them (ADR-0153 D4).
	// Raw here and normalized by vaultEntries, because one field with two shapes is what
	// keeps multi-identity from being a second field with a precedence rule (§2.4).
	Vault json.RawMessage `json:"vault,omitempty"`

	// Connection is the authentication half of reachability (ansible.input.v6,
	// ADR-0126 D1). Its absence is legitimate — a local-connection target needs no
	// credential — so it is a pointer, and connectionVars tolerates nil.
	Connection *connectionParams `json:"connection,omitempty"`

	// NOTE: `check` and `eeImage` are deliberately ABSENT here even though the
	// Contract still carries them (deprecated). Check-mode is the port's DryRun bit
	// (ADR-0051 MF6 / ADR-0117 D2); per-Step EE selection is by Actuator declaration
	// (ADR-0117 D3a). Reading them here would re-create the lying seam v5 documents.
}

// becomeParams is privilege escalation as a declared, reviewable value (ADR-0117 D1).
type becomeParams struct {
	Enabled bool   `json:"enabled"`
	User    string `json:"user,omitempty"`
	Method  string `json:"method,omitempty"`
	// PasswordRef closes ANS-010 (ADR-0153 D5). Rendered as --become-password-file: the
	// path, never the password. A target whose escalation prompts could not escalate at all
	// before this field existed.
	PasswordRef *passwordRef `json:"passwordRef,omitempty"`
}

// passwordRef is a brokered password: a CredentialRef already on the Step, resolved to its
// MOUNT PATH and handed to ansible as a --*-password-file argument (ADR-0153 D3).
//
// The path is the whole point. Rendering the password as an inventory group var instead —
// the shape everybody writes first — puts secret material in inventory/hosts, which
// writeInventory creates at 0644 in the private data dir BESIDE ansible-runner's
// artifacts/. §2.5 says material is never written to artifacts, so that shape is not a
// weaker option, it is a forbidden one.
type passwordRef struct {
	CredentialRef string `json:"credentialRef"`
	File          string `json:"file,omitempty"`
}

// vaultParams points at a CredentialRef ALREADY on the Step (§2.5): the use-grant
// check at dispatch stays the single authorization path. File is optional — omitted
// when the ref injects exactly one file.
type vaultParams struct {
	CredentialRef string `json:"credentialRef"`
	File          string `json:"file,omitempty"`
	// ID is the vault IDENTITY (ADR-0153 D4, closing ANS-011). With it the entry renders
	// --vault-id <id>@<path>; without it, --vault-password-file <path>, byte-identical to v7.
	ID string `json:"id,omitempty"`
}

// vaultEntries normalizes params.vault's two shapes into one list. A single object is the
// v7 form and MUST keep working: the registry keeps one live actuators/ansible.input and a
// Step cannot pin a version (ADR-0132 D4), so an array-only read would fail every shipped
// object-form Step the moment v8 landed.
func vaultEntries(raw json.RawMessage) ([]vaultParams, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one vaultParams
	if err := json.Unmarshal(raw, &one); err == nil {
		return []vaultParams{one}, nil
	}
	var many []vaultParams
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("params.vault is neither one entry nor a list of them: %w", err)
	}
	seen := map[string]bool{}
	for _, v := range many {
		if v.ID == "" {
			continue
		}
		if seen[v.ID] {
			// Two files claiming one identity is an ambiguity ansible resolves by ORDER —
			// a silent winner by another name (§2.4).
			return nil, fmt.Errorf("params.vault declares vault id %q twice — one identity, one password file", v.ID)
		}
		seen[v.ID] = true
	}
	return many, nil
}

// scmParams is a git content-ref: the repo to clone in the EE and the playbook path
// within it (charter §5.6, ADR-0025).
type scmParams struct {
	Repo     string `json:"repo"`
	Ref      string `json:"ref,omitempty"`
	Playbook string `json:"playbook"`
}

// credentialsMount is where file-injected CredentialRefs land in the EE pod
// (dispatch mounts each ref at /runner/credentials/<refName>/, items 0400).
const credentialsMount = "/runner/credentials"

// vaultPasswordFile resolves a vault CredentialRef to its in-pod file path. When the
// ref injects exactly one file the name is inferred; otherwise the Step must name it
// (params.vault.file) and the failure says so rather than guessing (§1.8).
func vaultPasswordFile(v *vaultParams, readDir func(string) ([]string, error)) (string, error) {
	return credentialFile("vault", v.CredentialRef, v.File, "params.vault.file", readDir)
}

// osReadDirNames lists the entry names of dir — the production vaultPasswordFile lister.
func osReadDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// playbookFlags renders the v5 typed run knobs (ADR-0117 D1) into ansible-playbook
// flags. EVERY token comes from a Contract-bounded value — enums, patterns, and
// integer ranges — and each is appended as its own token, never concatenated from
// operator-supplied text. That distinction is the whole argument for typing these
// fields instead of accepting a `cmdline` string (§1.1): there is no position at
// which an operator value can introduce a new flag or a shell metacharacter.
//
// dryRun is the port's check-mode bit (ADR-0051 MF6), NOT a param — it always wins
// and always implies --diff.
func playbookFlags(p params, dryRun bool, readDir func(string) ([]string, error)) ([]string, error) {
	var f []string
	if dryRun {
		f = append(f, "--check", "--diff")
	} else if p.Diff {
		f = append(f, "--diff") // apply-and-show-me — distinct from check-mode
	}
	if p.Become != nil {
		if p.Become.Enabled {
			f = append(f, "--become")
		}
		if p.Become.User != "" {
			f = append(f, "--become-user", p.Become.User)
		}
		if p.Become.Method != "" {
			f = append(f, "--become-method", p.Become.Method)
		}
		if p.Become.PasswordRef != nil {
			if !p.Become.Enabled {
				// One of the two is a mistake, and guessing which yields either a pointless
				// credential mount or a run that quietly does not escalate (ADR-0153 D5).
				return nil, fmt.Errorf("become.passwordRef is set but become.enabled is false — " +
					"an escalation password for an escalation nobody requested")
			}
			path, err := credentialFile("become", p.Become.PasswordRef.CredentialRef,
				p.Become.PasswordRef.File, "params.become.passwordRef.file", readDir)
			if err != nil {
				return nil, err
			}
			f = append(f, "--become-password-file", path)
		}
	}
	// The login/device password. A PATH, never the secret: --connection-password-file is
	// what makes that possible without a Jinja indirection (ADR-0153 D3).
	if p.Connection != nil && p.Connection.PasswordRef != nil {
		path, err := credentialFile("connection", p.Connection.PasswordRef.CredentialRef,
			p.Connection.PasswordRef.File, "params.connection.passwordRef.file", readDir)
		if err != nil {
			return nil, err
		}
		f = append(f, "--connection-password-file", path)
	}
	if p.Limit != "" {
		// Narrows the core-resolved set; it can never widen it — the rendered
		// inventory is the View's targets (ADR-0051 MF4).
		f = append(f, "--limit", p.Limit)
	}
	if len(p.Tags) > 0 {
		f = append(f, "--tags", strings.Join(p.Tags, ","))
	}
	if len(p.SkipTags) > 0 {
		f = append(f, "--skip-tags", strings.Join(p.SkipTags, ","))
	}
	if p.Forks > 0 {
		f = append(f, "--forks", strconv.Itoa(p.Forks))
	}
	if p.Timeout > 0 {
		f = append(f, "--timeout", strconv.Itoa(p.Timeout))
	}
	if p.Verbosity > 0 {
		f = append(f, "-"+strings.Repeat("v", p.Verbosity))
	}
	vaults, err := vaultEntries(p.Vault)
	if err != nil {
		return nil, err
	}
	for i := range vaults {
		path, err := vaultPasswordFile(&vaults[i], readDir)
		if err != nil {
			return nil, err
		}
		if id := vaults[i].ID; id != "" {
			f = append(f, "--vault-id", id+"@"+path)
			continue
		}
		f = append(f, "--vault-password-file", path)
	}
	return f, nil
}

// validateSCM rejects a content-ref that would fail (or be exploited) in-pod: a repo
// or ref beginning with "-" is parsed by git as an OPTION not a URL (argument
// injection that survives shell-quoting), and a playbook path must stay within the
// cloned repo (no traversal, no absolute path). Defense in depth with gitClone's "--".
func validateSCM(s *scmParams) error {
	if s.Repo == "" || s.Playbook == "" {
		return fmt.Errorf("params.scm requires repo and playbook")
	}
	if strings.HasPrefix(s.Repo, "-") {
		return fmt.Errorf("params.scm.repo must not begin with '-'")
	}
	if strings.HasPrefix(s.Ref, "-") {
		return fmt.Errorf("params.scm.ref must not begin with '-'")
	}
	if strings.Contains(s.Playbook, "..") || strings.HasPrefix(s.Playbook, "/") {
		return fmt.Errorf("params.scm.playbook must be a relative path within the repo")
	}
	return nil
}

// validatePlaybookPath keeps a mounted-tree playbook reference inside project/. The
// Contract already bounds the same value with a segment pattern, and this re-checks it
// anyway: the shim's Contract is the OPAQUE desired payload (§1.5), so it never assumes a
// validator ran upstream — the same reason validateSCM re-checks a path git would also
// reject. The path reaches an ansible-runner argument, so "-" is refused too: a leading
// dash is parsed as an option, which is the argument-injection shape validateSCM guards.
func validatePlaybookPath(playbook string) error {
	if strings.HasPrefix(playbook, "/") || strings.Contains(playbook, "..") {
		return fmt.Errorf("params.playbook must be a relative path within the mounted project tree")
	}
	if strings.HasPrefix(playbook, "-") {
		return fmt.Errorf("params.playbook must not begin with '-'")
	}
	return nil
}

// cloner fetches an SCM content-ref INTO projectDir — injectable so SCM handling is
// unit-tested without git (which, like ansible-runner, is a subprocess in the EE,
// never linked into the Apache core).
type cloner func(ctx context.Context, projectDir string, scm scmParams) error

// gitClone is the production cloner: a shallow clone of the ref, "--" stopping git's
// option parsing (belt to validateSCM's "-" guard).
func gitClone(ctx context.Context, projectDir string, scm scmParams) error {
	args := []string{"clone", "--depth", "1"}
	if scm.Ref != "" {
		args = append(args, "--branch", scm.Ref)
	}
	args = append(args, "--", scm.Repo, projectDir)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Execute is the production entry (the cmd calls it): render + run `bin`
// (ansible-runner) + stream typed shapes. Tests call Run with a fake runner.
func Execute(ctx context.Context, w io.Writer, dir, bin string, req Request) error {
	return Run(ctx, w, dir, req, execRunner{bin: bin}, gitClone)
}

// Run renders the request's inventory + play, runs ansible-runner, and emits the
// sovereign port's typed shapes as proto-JSON ApplyResponse lines to w (ADR-0051):
// per-host ItemResult (the fan-out), facts write-back keyed by the target's identity,
// check-mode drift, and — for every non-event line (banners/tracebacks) — a
// diagnostic TaskEvent (MF5, never dropped). A required terminal ends the stream.
func Run(ctx context.Context, w io.Writer, dir string, req Request, run commandRunner, clone cloner) error {
	var p params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return emitFatal(w, "invalid params: "+err.Error())
		}
	}
	// Playbook source: an SCM content-ref is cloned in the EE and the play runs FROM
	// the repo (playbook path validated within it); otherwise the inline play (or the
	// default gather play) is written to project/play.yml.
	// The connection's ansible_* vars, rendered HERE by the shim (§1.4) from the typed
	// v6 block, replacing the hand-written extraVars every Workflow used to carry. The
	// known-hosts file is per-Run and inside the runner dir: accept-new is worth
	// nothing without somewhere to remember the key, which is exactly what the old
	// `UserKnownHostsFile=/dev/null` guaranteed (ADR-0126 D2 — cross-Run memory is the
	// booked follow-up, and is deliberately NOT claimed here).
	chain, cherr := jumpChainOf(req.Targets)
	if cherr != nil {
		return emitFatal(w, cherr.Error())
	}
	// The OBSERVED transports, checked BEFORE anything runs (ADR-0156): every target's
	// coordinates parse and render, and this EE carries the collection and binary each
	// transport needs. One pass over the whole set rather than per-target lazily — a Run that
	// converges three hosts and then dies on the fourth's missing collection has already
	// changed three machines.
	if terr := validateTransports(req.Targets, p.Connection, osReadFile, osLookPath); terr != nil {
		return terr
	}
	// D5: a Step-declared connection.type and an observed transport are refused TOGETHER,
	// never resolved. Two homes for one fact is the precedence §2.4 refuses.
	if terr := refuseTransportAndDeclaredType(p.Connection, req.Targets); terr != nil {
		return terr
	}
	// ADR-0158 D1/D3: a target NOTHING observed and NOTHING declared has an unknown reach
	// method, and it is refused here rather than rendered as ssh. LAST of the four axes on
	// purpose — the three above are a defect in the image, in the coordinates, or in a brokered
	// credential, and this one is the estate not having said anything at all, which is the least
	// specific thing to report when more than one applies.
	if terr := requireReachMethod(req.Targets, p.Connection); terr != nil {
		return terr
	}
	connVars, cerr := connectionVars(p.Connection, chain, filepath.Join(dir, "known_hosts"), hasLocalTarget(req.Targets), osReadDirNames, osReadFile, stageKeyIn(dir))
	if cerr != nil {
		return emitFatal(w, cerr.Error())
	}
	inventory := renderInventory(req.Targets, connVars)

	playbook := "play.yml"
	switch {
	case p.SCM != nil && p.Playbook != "":
		// project/ has room for one content source, and a merge between two would need a
		// winner. The Contract refuses this pair; say so here too rather than silently
		// preferring one, because the shim is where an operator is reading logs (§1.8).
		return emitFatal(w, "params.scm and params.playbook are mutually exclusive — scm clones project/, playbook runs a file the core mounted there")
	case p.SCM != nil:
		if err := validateSCM(p.SCM); err != nil {
			return emitFatal(w, err.Error())
		}
		if err := writeInventory(dir, inventory, p.ExtraVars); err != nil {
			return emitFatal(w, err.Error())
		}
		if err := clone(ctx, filepath.Join(dir, "project"), *p.SCM); err != nil {
			return emitFatal(w, "git clone: "+err.Error())
		}
		playbook = p.SCM.Playbook
	case p.Playbook != "":
		// project/ is ALREADY populated — the core mounted the Actuator's declared content
		// root there (ADR-0134). So this lays out only the source-independent parts, exactly
		// as the SCM branch does, and never writes project/play.yml over a mounted tree.
		if err := validatePlaybookPath(p.Playbook); err != nil {
			return emitFatal(w, err.Error())
		}
		if err := writeInventory(dir, inventory, p.ExtraVars); err != nil {
			return emitFatal(w, err.Error())
		}
		// Existence is checked HERE as well as at estate load, because the two answer
		// different questions: the load knows what the declaration said, this knows what
		// the pod actually received. A mount that silently did not happen would otherwise
		// surface as ansible-runner's own "playbook could not be found", which names
		// neither the Actuator nor the fact that a mount is involved (§1.8).
		if _, err := os.Stat(filepath.Join(dir, "project", filepath.FromSlash(p.Playbook))); err != nil {
			return emitFatal(w, "params.playbook "+p.Playbook+" is not in the mounted project tree: "+err.Error())
		}
		playbook = p.Playbook
	default:
		play := p.Play
		if play == "" {
			play = GatherFactsPlay
		}
		if err := writeContent(dir, play, inventory, p.ExtraVars); err != nil {
			return emitFatal(w, err.Error())
		}
	}
	args := []string{"run", dir, "-p", playbook, "-j"}
	// MF6: check-mode is the port DryRun bit, mapped here — not a core param. The v5
	// run knobs (ADR-0117 D1) join it on the same cmdline; every token is
	// Contract-bounded, so the joined string can carry no operator-authored flag or
	// metacharacter (playbookFlags).
	flags, ferr := playbookFlags(p, req.DryRun, osReadDirNames)
	if ferr != nil {
		return emitFatal(w, ferr.Error())
	}
	if len(flags) > 0 {
		args = append(args, "--cmdline", strings.Join(flags, " "))
	}

	byHost := make(map[string]Target, len(req.Targets))
	for _, t := range req.Targets {
		byHost[t.Name] = t
	}
	emit := pluginserve.NewEmitter(w).Send

	// HOW THIS RUN TRIED TO CONNECT, stated once, as Run metadata (§1.8 — the abstraction must
	// never hide diagnosis).
	//
	// It was missing and it cost three full capstone runs. A connection failure surfaces as
	// ansible's `unreachable: Failed to create temporary directory … did not have permissions on
	// the target directory` — a message that names the GUEST for what may be a control-node
	// problem, a missing credential, an unobserved transport, or a plain ssh failure. All four
	// render the same line, and the one artifact that distinguishes them — the inventory the shim
	// rendered — lived only inside a pod that is gone by the time anyone reads the Run.
	//
	// SAFE TO EMIT, and that is a property of ADR-0153 D3 rather than of this call: every
	// credential in the inventory is a PATH. Passwords are `--connection-password-file`, keys are
	// staged file paths, the kubeconfig is its mount. There is no value here to redact — which is
	// the same design that keeps them out of `inventory/hosts` at 0644 beside the artifacts.
	emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(),
		Message: "connection plan — the rendered inventory:\n" + inventory,
		Scope:   pluginv1.TaskEvent_SCOPE_RUN,
		Fields:  map[string]string{"kind": "inventory"},
	}})

	// What content this EE actually carries (ADR-0117 D3), stated once per Run so the
	// descent can answer "which collections/roles did this Run have?" without inspecting
	// an image digest by hand (§1.8).
	//
	// SCOPE_RUN (ADR-0121) is what makes that answerable without the interface plane
	// learning the word "ee-content". This event describes the image the whole Run executed
	// in, not any task in it — so a descent surface pins it as Run metadata by reading a
	// spine-owned field, the same way it already reads Level. ADR-0117 follow-up (j) was
	// refused as originally written precisely because the alternative was a `kind` match in
	// the UI, which is the `if ansible{}` §1.4 forbids, relocated into TypeScript.
	emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: contentSummary(os.ReadFile), At: timestamppb.Now(),
		Scope:  pluginv1.TaskEvent_SCOPE_RUN,
		Fields: map[string]string{"kind": "ee-content"},
	}})

	// actuated records every host that produced a terminal per-host result. A run
	// that actuates NOTHING is the vacuous-success hole (§1.8): ansible exits 0 when
	// a play's `hosts:` pattern matches no inventory host, so the Run would fold
	// green having changed nothing. Counted here — in the content-expertise — because
	// only the ansible plugin knows a play can no-op; the spine stays content-blind.
	actuated := map[string]bool{}
	// outputs is the play's typed cross-Step payload, carried to the terminal message (CERT-2).
	var outputs []byte
	noHostsMatched := false
	// unparsedEvents counts lines that WERE ansible-runner events but failed to decode.
	// Such a line loses its ItemResult / facts / drift, so the shim no longer knows what
	// happened on that host — and must not then assert a cause it cannot support (§1.8).
	unparsedEvents := 0

	onLine := func(line []byte) {
		ev, ok := parseEvent(line)
		if !ok {
			// MF5: banners / python tracebacks / stderr → typed diagnostic, never dropped.
			level, kind := pluginv1.TaskEvent_LEVEL_INFO, "diagnostic"
			if eventShaped(line) {
				// Not a banner: a real event whose typed signal we just LOST. WARN under
				// its own kind so it is visible as a DEFECT during descent rather than
				// hiding among ordinary output — the failure mode that let the float64
				// overflow in parseEvent go unnoticed until a crypto play hit it.
				level, kind = pluginv1.TaskEvent_LEVEL_WARN, "unparsed-event"
				unparsedEvents++
			}
			emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
				Level: level, Message: string(line), At: timestamppb.Now(),
				Fields: map[string]string{"kind": kind},
			}})
			return
		}
		host, _ := ev.EventData["host"].(string)
		// A play whose pattern matched nothing is ansible's OWN signal that a play
		// no-opped. Surfaced at WARN (not INFO) so it is visible during descent even
		// when OTHER plays in the same playbook did run — the partial-vacuity case
		// the terminal check below cannot see.
		level := eventSeverity(ev)
		if isNoHostsMatched(ev) {
			noHostsMatched, level = true, pluginv1.TaskEvent_LEVEL_WARN
		}
		// Carry ansible's own account of what happened. The event NAME alone left a
		// failure saying only THAT it failed — and the pod's logs go with the Job, so
		// the reason was gone for good (§1.8).
		msg := ev.Event
		if reason := eventReason(ev); reason != "" {
			msg += ": " + reason
		}
		emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
			Level: level, Message: msg, At: timestamppb.Now(),
			Fields: map[string]string{"host": host, "kind": ev.Event},
		}})
		if h, st := hostStatus(ev); st != pluginv1.ItemResult_STATUS_UNSPECIFIED && h != "" {
			// Counted only if h is IN the core-resolved set. A play using `hosts:
			// localhost` (ansible's implicit localhost, absent from the rendered
			// inventory) produces results the hub then rejects as a confused deputy
			// (MF4) — so it must not satisfy the actuation check either, or a run that
			// touched nothing in the View would still read as green.
			if _, ok := byHost[h]; ok {
				actuated[h] = true
			}
			emit(&pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: h, Status: st}})
		}
		// A play's typed OUTPUTS ride to the terminal message rather than being emitted here: the
		// port validates them against a pinned contract once, on the terminal, exactly as an
		// Action's are. Last writer wins across hosts, which is right for the single-target flows
		// that use them (a CSR belongs to one host) and is stated rather than left implicit.
		if o, diag := extractOutputs(factsOf(ev)); o != nil {
			outputs = o
			// Say WHAT was published, where the play ran. Field NAMES only — a value here could
			// be anything the play chose to publish, and the shim's event stream is not the place
			// to decide it is safe to print (§2.5).
			emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
				Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(),
				Message: "published outputs: " + strings.Join(outputFields(o), ", "),
				Fields:  map[string]string{"host": host, "kind": "outputs"},
			}})
		} else if diag != "" {
			// PUBLISHED BUT UNUSABLE. Loud, at the producer, naming the shape — never dropped to
			// be rediscovered as a template error in a later Step.
			emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
				Level: pluginv1.TaskEvent_LEVEL_WARN, At: timestamppb.Now(),
				Message: "outputs discarded: " + diag,
				Fields:  map[string]string{"host": host, "kind": "outputs-discarded"},
			}})
		}
		if facets := extractFacts(ev); facets != nil {
			// Facts project onto the host's Entity by the target's IDENTITY (the hub
			// resolves-by-identity + gates the facet namespaces on the grant, MF3).
			emit(&pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
				Kind: "host", IdentityKeys: byHost[host].Identity, Facets: facets,
			}}})
		}
		if d := extractDiff(ev); d != nil {
			emit(&pluginv1.ApplyResponse{Drift: &pluginv1.DiffFragment{
				ItemKey: host, Detail: &pluginv1.Payload{Bytes: d},
			}})
		}
	}

	rc, err := run.run(ctx, dir, args, onLine)
	if err != nil {
		return emitFatal(w, "ansible-runner: "+err.Error())
	}
	// Required terminal (MF5): a shim that reaches here emits it; the HUB folds
	// Succeeded from the per-host ItemResults + this terminal, never from ok alone.
	// A zero-actuation run is FAILED here rather than reported green (§1.8).
	ok, msg := rc == 0, fmt.Sprintf("ansible-runner rc=%d", rc)
	if vac := vacuousRun(rc, req.Targets, len(actuated), p.Limit, noHostsMatched, unparsedEvents); vac != "" {
		ok, msg = false, vac
	}
	term := &pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg,
	}}
	// Outputs ride the TERMINAL message and are governed HUB-SIDE against the Actuator's pinned
	// output contract (CERT-2). The shim asserts no contract id of its own, deliberately: the pin
	// belongs to the core, and a tool naming its own contract would be the plugin deciding what a
	// consumer may bind — the inversion §1.5 refuses. An Actuator with no pin gets these refused,
	// which is the correct answer to "a shape nobody agreed to".
	if outputs != nil {
		term.Outputs = &pluginv1.Payload{Bytes: outputs}
	}
	emit(term)
	return nil
}

// isNoHostsMatched reports ansible's own "this play targeted nothing" event.
func isNoHostsMatched(ev RunnerEvent) bool {
	return ev.Event == "playbook_on_no_hosts_matched" || ev.Event == "playbook_on_no_hosts_remaining"
}

// vacuousRun returns a terminal failure message when ansible exited 0 having
// actuated NO host from a non-empty resolved target set — otherwise "".
//
// This closes a §1.8 hole, not a cosmetic one, and the rc=0 path is LIVE-VERIFIED
// against the EE image rather than assumed: a play whose `hosts:` pattern names
// something absent from the rendered inventory emits `playbook_on_no_hosts_matched`
// and `ansible-runner` exits **0**. Without this check the Run folded SUCCEEDED —
// zero hosts means zero per-target failures — while having changed nothing.
//
// What is deliberately NOT claimed: `params.limit` narrowing the host list to EMPTY
// is *not* an rc=0 path. Ansible raises "Specified inventory, host pattern and/or
// --limit leaves us with no hosts to target" and exits **1**, so that case already
// failed loudly (also verified live). `limit` can still contribute by being disjoint
// from the play's pattern, so it is named only when set — never as the cause.
//
// The message branches on ansible's own signal rather than asserting a cause it did
// not observe: with `noHostsMatched` the pattern demonstrably matched nothing; without
// it the play matched but produced no per-host result (e.g. a play with no tasks), a
// materially different diagnosis.
//
// Deliberately NOT "every target produced a result": `limit` narrowing 3 targets to
// 1 is a legitimate, requested narrowing. Only actuating NOTHING is vacuous.
//
// unparsedEvents takes PRECEDENCE over every other cause: when the shim failed to
// decode events it does not know whether hosts were actuated, so blaming the play's
// pattern would be asserting an unobserved cause. It still fails the Run — "I cannot
// tell what happened" is not a success — but names the real, different reason. This
// case is not hypothetical: the float64 overflow fixed in parseEvent made a successful
// one-task openssl_privatekey play report zero actuation, and this check then declared
// that success a failure with the wrong explanation.
func vacuousRun(rc int, targets []Target, actuated int, limit string, noHostsMatched bool, unparsedEvents int) string {
	if rc != 0 || actuated > 0 || len(targets) == 0 {
		return ""
	}
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	cause := "the play ran but produced no result for any of them (a play with no tasks does this)"
	switch {
	case unparsedEvents > 0:
		cause = fmt.Sprintf("%d ansible event(s) could not be DECODED by this shim, so per-host results were lost and actuation is unknown — this is a shim defect, not necessarily a problem with the play (see the unparsed-event diagnostics above)", unparsedEvents)
	case noHostsMatched:
		cause = "ansible reported no hosts matched — the play's `hosts:` pattern names nothing in the inventory, which holds exactly these targets (so `hosts: all` always matches)"
	}
	msg := fmt.Sprintf("ansible-runner rc=0 but NO host was actuated out of the %d resolved target(s) [%s]: %s. This run changed nothing and is not a success",
		len(targets), strings.Join(names, " "), cause)
	if limit != "" {
		msg += fmt.Sprintf("; params.limit=%q is also set — check it is not disjoint from the play's pattern", limit)
	}
	return msg
}

// emitFatal writes a terminal not-ok diagnostic and returns nil (a domain failure
// rides the typed channel, §1.8, not a transport error).
func emitFatal(w io.Writer, msg string) error { return pluginserve.NewEmitter(w).Fatal(msg) }

// writeInventory lays out the parts of the ansible-runner private-data-dir that are
// independent of the playbook source: inventory/hosts and optional env/extravars
// (never secret material, §2.5). It deliberately does NOT create project/ — an SCM
// clone populates that dir itself (git requires an empty/absent target).
func writeInventory(dir, inventory string, extraVars map[string]any) error {
	for _, sub := range []string{"inventory", "env", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "inventory", "hosts"), []byte(inventory), 0o644); err != nil {
		return err
	}
	if len(extraVars) > 0 {
		raw, err := json.Marshal(extraVars)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "env", "extravars"), raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeContent lays out the private-data-dir for an INLINE play: the inventory parts
// plus project/play.yml (the SCM path clones project/ instead, §5.6).
func writeContent(dir, play, inventory string, extraVars map[string]any) error {
	if err := writeInventory(dir, inventory, extraVars); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "project"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "project", "play.yml"), []byte(play), 0o644)
}
