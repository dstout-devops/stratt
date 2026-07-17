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
}

// scmParams is a git content-ref: the repo to clone in the EE and the playbook path
// within it (charter §5.6, ADR-0025).
type scmParams struct {
	Repo     string `json:"repo"`
	Ref      string `json:"ref,omitempty"`
	Playbook string `json:"playbook"`
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
	if req.DryRun { // MF6: check-mode is the port DryRun bit, mapped here — not a core param
		args = append(args, "--cmdline", "--check --diff")
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

	onLine := func(line []byte) {
		ev, ok := parseEvent(line)
		if !ok {
			// MF5: banners / python tracebacks / stderr → typed diagnostic, never dropped.
			emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
				Level: pluginv1.TaskEvent_LEVEL_INFO, Message: string(line), At: timestamppb.Now(),
				Fields: map[string]string{"kind": "diagnostic"},
			}})
			return
		}
		host, _ := ev.EventData["host"].(string)
		emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, Message: ev.Event, At: timestamppb.Now(),
			Fields: map[string]string{"host": host, "kind": ev.Event},
		}})
		if h, st := hostStatus(ev); st != pluginv1.ItemResult_STATUS_UNSPECIFIED && h != "" {
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
	emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Terminal: true, Ok: rc == 0, At: timestamppb.Now(),
		Message: fmt.Sprintf("ansible-runner rc=%d", rc),
	}})
	return nil
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
