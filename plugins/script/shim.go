package script

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// commandRunner runs the user script (interpreter scriptPath) with env, streaming
// each combined stdout/stderr line to onLine, and returns the process exit code.
// Injectable so the shim's mapping is unit-tested without a real subprocess.
type commandRunner interface {
	run(ctx context.Context, interpreter, scriptPath string, env []string, onLine func(line string)) (rc int, err error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, interpreter, scriptPath string, env []string, onLine func(line string)) (int, error) {
	cmd := exec.CommandContext(ctx, interpreter, scriptPath)
	cmd.Env = append(os.Environ(), env...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	cmd.Stderr = cmd.Stdout // combined — the per-line output is §1.8 observability, not governed
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		onLine(sc.Text())
	}
	if werr := cmd.Wait(); werr != nil {
		if exitErr, ok := werr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil // non-zero is a RESULT (rc), not a spawn error
		}
		return -1, werr
	}
	return 0, nil
}

// Execute is the production entry (the cmd calls it): write the script + run it once
// per target, emitting the port's typed shapes. Tests call Run with a fake runner.
func Execute(ctx context.Context, w io.Writer, dir string, req Request) error {
	return Run(ctx, w, dir, req, execRunner{})
}

// Run writes the user script and runs it once per core-resolved target (ADR-0051),
// emitting the sovereign port's typed shapes as proto-JSON ApplyResponse lines: one
// ItemResult per target (the fan-out, keyed by the LEGIBLE target name so the hub's
// confused-deputy gate binds), each script output line as a diagnostic TaskEvent
// (§1.8, never dropped), and a required terminal whose ok folds the per-target rc's.
func Run(ctx context.Context, w io.Writer, dir string, req Request, run commandRunner) error {
	var p params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return emitFatal(w, "invalid params: "+err.Error())
		}
	}
	if p.Script == "" {
		return emitFatal(w, "params.script is required")
	}
	interp, ok := interpreterFor(p)
	if !ok {
		return emitFatal(w, "unsupported interpreter "+p.Interpreter+" (sh, python3)")
	}
	scriptPath := filepath.Join(dir, "script")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return emitFatal(w, err.Error())
	}
	if err := os.WriteFile(scriptPath, []byte(p.Script), 0o755); err != nil {
		return emitFatal(w, err.Error())
	}

	emit := func(r *pluginv1.ApplyResponse) {
		if b, err := protojson.Marshal(r); err == nil {
			_, _ = w.Write(b)
			_, _ = w.Write([]byte("\n"))
		}
	}
	event := func(host, kind, msg string) {
		emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, Message: msg, At: timestamppb.Now(),
			Fields: map[string]string{"host": host, "kind": kind},
		}})
	}

	failed := 0
	for _, t := range req.Targets {
		event(t.Name, "target_started", "")
		rc, err := run.run(ctx, interp, scriptPath, scriptEnv(t), func(line string) {
			event(t.Name, "target_output", line)
		})
		if err != nil {
			// A spawn failure is a FAILED target (surfaced as a diagnostic), not a
			// transport error — a domain failure rides the typed channel (§1.8).
			event(t.Name, "diagnostic", "run script: "+err.Error())
			emit(&pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: t.Name, Status: pluginv1.ItemResult_STATUS_FAILED}})
			failed++
			continue
		}
		status := pluginv1.ItemResult_STATUS_OK
		if rc != 0 {
			status = pluginv1.ItemResult_STATUS_FAILED
			failed++
		}
		emit(&pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: t.Name, Status: status}})
		event(t.Name, "target_finished", fmt.Sprintf("rc=%d", rc))
	}
	// Required terminal (MF5): the hub folds Succeeded from the per-target ItemResults
	// + this terminal, never from ok alone.
	emit(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Terminal: true, Ok: failed == 0, At: timestamppb.Now(),
		Message: fmt.Sprintf("%d target(s), %d failed", len(req.Targets), failed),
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
