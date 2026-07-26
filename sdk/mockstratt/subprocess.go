package mockstratt

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
	"sync"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The EE-Job (subprocess) transport (ADR-0051): a ONE-SHOT binary that reads the
// sovereign ApplyRequest from its runner directory and emits proto-JSON
// ApplyResponse lines on stdout. In production the dispatcher builds that directory
// from a ConfigMap, spawns a K8s Job, decodes the typed stdout, and feeds the
// hub-side governor. This reproduces all four steps against a temp dir and
// os/exec — which is the entire difference between "I need a cluster to test my
// plugin" and "I need a temp dir".

// Subprocess drives a one-shot plugin binary over the EE-Job transport.
type Subprocess struct {
	// Binary is the plugin executable (the EE image's entrypoint, e.g.
	// stratt-ansible). Required.
	Binary string
	// Args are extra arguments after the binary, mirroring an Actuator's declared
	// jobCommand tail.
	Args []string
	// Dir is the runner directory. Empty ⇒ a fresh temp dir per Run, removed
	// afterwards. Set it when you want to inspect what the plugin was handed —
	// which is the second question every plugin author asks after "why did it fail".
	Dir string
	// Env is appended to the child's environment. STRATT_REQUEST and
	// STRATT_RUNNER_DIR are set for you; anything here overrides them, deliberately,
	// because a plugin free to name its own request path must be testable that way.
	Env []string
	// DryRunnable mirrors the Actuator declaration. The core refuses a dry-run
	// request for an Actuator that did not declare it (MF6) — CORE-SIDE, before the
	// plugin is spawned, because a shim that silently ignored the check bit would
	// run live side effects. Run refuses it here for the same reason and at the same
	// point.
	DryRunnable bool
	// ReadOnlyProject mounts project/ read-only, as the ConfigMap volume does.
	//
	// This is fidelity that has already earned its keep: ADR-0134 had to RESERVE the
	// name play.yml precisely because the inline path writes project/play.yml, which
	// would hit EACCES against a real mount. A plugin that writes into its own
	// content root passes on a writable temp dir and fails in a pod.
	//
	// HONEST LIMIT: this is a mode bit, so a plugin running as root ignores it. It
	// is a strong hint, not a guarantee — do not read a pass here as proof.
	ReadOnlyProject bool
}

// Run executes the plugin against req and returns the GOVERNED result.
//
// Both failure signals are folded, exactly as executeJobPlugin folds them: the
// governor's terminal verdict AND the process exit status. A green terminal
// followed by a non-zero exit (an OOM kill, a torn cleanup, a serialize error after
// the last frame) must read NOT-OK — otherwise the most alarming class of failure
// there is, "it said it worked and then died", reports as success.
func (s *Subprocess) Run(ctx context.Context, host *Host, req Request) (Result, error) {
	if s.Binary == "" {
		return Result{}, fmt.Errorf("mockstratt: Subprocess.Binary is required")
	}
	if req.DryRun && !s.DryRunnable {
		// Core-side refusal, before spawn — see DryRunnable.
		return Result{}, fmt.Errorf("mockstratt: dry-run requested but this actuator does not declare dryRunnable (MF6)")
	}

	dir := s.Dir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "mockstratt-runner-")
		if err != nil {
			return Result{}, fmt.Errorf("mockstratt: runner dir: %w", err)
		}
		dir = tmp
		defer func() {
			// Restore write permission first: a read-only project/ would otherwise
			// defeat the removal and leak a temp tree per run.
			_ = os.Chmod(filepath.Join(dir, "project"), 0o755)
			_ = os.RemoveAll(dir)
		}()
	}

	if err := s.stage(dir, req); err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, s.Binary, s.Args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"STRATT_REQUEST="+filepath.Join(dir, "stratt", "request.json"),
		"STRATT_RUNNER_DIR="+dir,
	)
	cmd.Env = append(cmd.Env, s.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("mockstratt: stdout pipe: %w", err)
	}
	// stderr is NOT merged into stdout. In a pod they are separate streams and only
	// stdout carries port frames; merging them here would let a plugin pass locally
	// by writing frames to stderr, which the real dispatcher never reads for them.
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("mockstratt: start %s: %w", s.Binary, err)
	}

	// The dispatcher pre-routes non-frame lines (runner banners, tracebacks) to the
	// §1.8 diagnostic ring, so the governor sees only frames. Same split here, and
	// the diagnostics are RETAINED rather than dropped: a plugin that dies before
	// its first frame has nothing else to say for itself.
	stream, diagnostics := newLineStream(stdout)

	// ORDER IS LOAD-BEARING: govern to EOF FIRST, then Wait.
	//
	// cmd.Wait closes the stdout pipe as soon as the process exits, so waiting
	// concurrently with the scanner races it — and the frame most likely to be lost
	// is the LAST one, which is the terminal. A dropped terminal folds a perfectly
	// good Run to "never terminated": the harness would invent failures and blame
	// the plugin. Govern returns when the scanner closes the channel at EOF, which
	// is exactly when the child's stdout is done.
	res, gerr := host.Govern(ctx, stream, req.Targets)
	stream.close()
	waitErr := cmd.Wait()
	if gerr != nil {
		return res, gerr
	}

	res.Diagnostics = diagnostics()
	if t := strings.TrimSpace(stderr.String()); t != "" {
		res.Diagnostics = append(res.Diagnostics, "stderr: "+t)
	}
	// The exit-status half of the fold. A non-zero exit is retained as the Error
	// only when the plugin did not already say something better — its own account
	// of the failure beats an exit code every time (§1.8).
	if waitErr != nil {
		res.Succeeded = false
		if res.Error == "" {
			res.Error = waitErr.Error()
		}
	}
	return res, nil
}

// stage builds the runner directory: the sovereign request the plugin reads, and
// the declared content root mounted at project/.
func (s *Subprocess) stage(dir string, req Request) error {
	raw, err := protojson.Marshal(req.ApplyRequest())
	if err != nil {
		return fmt.Errorf("mockstratt: marshal ApplyRequest: %w", err)
	}
	if err := writeFile(filepath.Join(dir, "stratt", "request.json"), raw, 0o644); err != nil {
		return err
	}

	if len(req.Content) == 0 {
		return nil
	}
	for rel, content := range req.Content {
		// The core copies a directory a DECLARATION named; it never inspects the
		// contents and neither does this. Cleaning the path is a containment check on
		// the harness's own filesystem, not an opinion about what belongs in a
		// project tree.
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			return fmt.Errorf("mockstratt: content path %q escapes project/", rel)
		}
		if err := writeFile(filepath.Join(dir, "project", clean), []byte(content), 0o644); err != nil {
			return err
		}
	}
	if s.ReadOnlyProject {
		if err := os.Chmod(filepath.Join(dir, "project"), 0o555); err != nil {
			return fmt.Errorf("mockstratt: read-only project/: %w", err)
		}
	}
	return nil
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mockstratt: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, mode); err != nil {
		return fmt.Errorf("mockstratt: write %s: %w", path, err)
	}
	return nil
}

// lineStream splits a plugin's stdout into port frames (for the governor) and
// everything else (for diagnostics).
//
// An UNDECODABLE line is a diagnostic, not an error. The real governor treats one
// as fatal because the dispatcher has already filtered non-frames upstream; here
// this IS that filter, and a plugin under development printing a stray fmt.Println
// should get a legible diagnostic rather than a decode error that names the wrong
// problem.
type lineStream struct {
	ch     chan *pluginv1.ApplyResponse
	once   sync.Once
	closed chan struct{}
}

func newLineStream(r io.Reader) (*lineStream, func() []string) {
	s := &lineStream{ch: make(chan *pluginv1.ApplyResponse, 64), closed: make(chan struct{})}
	var mu sync.Mutex
	var diags []string

	go func() {
		defer close(s.ch)
		sc := bufio.NewScanner(r)
		// Plugin frames carry gathered facts and drift fragments; the 64KiB default
		// truncates them into undecodable JSON, which would surface as a mystery
		// diagnostic rather than a size problem.
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			// Cheap pre-filter: only a JSON object can be a frame. Keeps a plain-text
			// banner from being reported as a protojson failure.
			if !json.Valid(line) || strings.TrimSpace(string(line))[0] != '{' {
				mu.Lock()
				diags = append(diags, string(line))
				mu.Unlock()
				continue
			}
			resp := &pluginv1.ApplyResponse{}
			if err := protojson.Unmarshal(line, resp); err != nil {
				mu.Lock()
				diags = append(diags, fmt.Sprintf("undecodable ApplyResponse (%v): %s", err, line))
				mu.Unlock()
				continue
			}
			select {
			case s.ch <- resp:
			case <-s.closed:
				return
			}
		}
		if err := sc.Err(); err != nil {
			mu.Lock()
			diags = append(diags, "stdout read error: "+err.Error())
			mu.Unlock()
		}
	}()

	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), diags...)
	}
}

func (s *lineStream) Recv() (*pluginv1.ApplyResponse, error) {
	resp, ok := <-s.ch
	if !ok {
		return nil, io.EOF
	}
	return resp, nil
}

func (s *lineStream) close() { s.once.Do(func() { close(s.closed) }) }
