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

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	Vault     *vaultParams  `json:"vault,omitempty"`

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
}

// vaultParams points at a CredentialRef ALREADY on the Step (§2.5): the use-grant
// check at dispatch stays the single authorization path. File is optional — omitted
// when the ref injects exactly one file.
type vaultParams struct {
	CredentialRef string `json:"credentialRef"`
	File          string `json:"file,omitempty"`
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
	dir := filepath.Join(credentialsMount, v.CredentialRef)
	if v.File != "" {
		return filepath.Join(dir, v.File), nil
	}
	names, err := readDir(dir)
	if err != nil {
		return "", fmt.Errorf("vault credentialRef %q is not mounted at %s — is it on the Step's credentialRefs? (%w)", v.CredentialRef, dir, err)
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("vault credentialRef %q injects no files at %s", v.CredentialRef, dir)
	case 1:
		return filepath.Join(dir, names[0]), nil
	default:
		return "", fmt.Errorf("vault credentialRef %q injects %d files (%s) — set params.vault.file to choose one", v.CredentialRef, len(names), strings.Join(names, ", "))
	}
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
	if p.Vault != nil {
		path, err := vaultPasswordFile(p.Vault, readDir)
		if err != nil {
			return nil, err
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
	playbook := "play.yml"
	if p.SCM != nil {
		if err := validateSCM(p.SCM); err != nil {
			return emitFatal(w, err.Error())
		}
		if err := writeInventory(dir, buildInventory(req.Targets), p.ExtraVars); err != nil {
			return emitFatal(w, err.Error())
		}
		if err := clone(ctx, filepath.Join(dir, "project"), *p.SCM); err != nil {
			return emitFatal(w, "git clone: "+err.Error())
		}
		playbook = p.SCM.Playbook
	} else {
		play := p.Play
		if play == "" {
			play = GatherFactsPlay
		}
		if err := writeContent(dir, play, buildInventory(req.Targets), p.ExtraVars); err != nil {
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
	emit := func(r *pluginv1.ApplyResponse) {
		if b, err := protojson.Marshal(r); err == nil {
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n"))
		}
	}

	// actuated records every host that produced a terminal per-host result. A run
	// that actuates NOTHING is the vacuous-success hole (§1.8): ansible exits 0 when
	// a play's `hosts:` pattern matches no inventory host, so the Run would fold
	// green having changed nothing. Counted here — in the content-expertise — because
	// only the ansible plugin knows a play can no-op; the spine stays content-blind.
	actuated := map[string]bool{}
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
		level := pluginv1.TaskEvent_LEVEL_INFO
		if isNoHostsMatched(ev) {
			noHostsMatched, level = true, pluginv1.TaskEvent_LEVEL_WARN
		}
		emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
			Level: level, Message: ev.Event, At: timestamppb.Now(),
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
	emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Terminal: true, Ok: ok, At: timestamppb.Now(), Message: msg,
	}})
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
func emitFatal(w io.Writer, msg string) error {
	r := &pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, Message: msg, Terminal: true, Ok: false, At: timestamppb.Now(),
	}}
	if b, err := protojson.Marshal(r); err == nil {
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n"))
	}
	return nil
}

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
