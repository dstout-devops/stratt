package opentofu

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
)

// commandRunner runs one tofu invocation in dir, streaming each stdout line to
// onLine, and returns the full stdout plus the process exit code. It is injectable
// so tests drive canned -json without a tofu binary — the tofu binary is a
// SUBPROCESS, never linked (charter §3: OpenTofu over Terraform, shelled out).
type commandRunner interface {
	run(ctx context.Context, dir string, env, args []string, onLine func([]byte)) (stdout []byte, rc int, err error)
}

// execRunner shells out to the real `tofu` binary.
type execRunner struct{ bin string }

func (e execRunner) run(ctx context.Context, dir string, env, args []string, onLine func([]byte)) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, -1, err
	}
	cmd.Stderr = cmd.Stdout // fatal diagnostics ride the same stream (never dropped, §1.8)
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	var full []byte
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 32<<20) // tofu plan/show -json lines can be large
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		full = append(append(full, line...), '\n')
		if onLine != nil {
			onLine(line)
		}
	}
	// A non-zero exit is a tofu RESULT (rc), not a runner error — the caller folds
	// it into the workspace ItemResult; only a spawn/pipe failure is a runner error.
	if werr := cmd.Wait(); werr != nil {
		if exitErr, ok := werr.(*exec.ExitError); ok {
			return full, exitErr.ExitCode(), nil
		}
		return full, -1, werr
	}
	return full, 0, nil
}

// Config is the plugin's process configuration.
type Config struct {
	PluginID string
	// TofuBin is the tofu executable (default "tofu").
	TofuBin string
	// ModuleRoot is the base directory holding module subdirs (params.Module is a
	// subdir under it). The plugin runs tofu there.
	ModuleRoot string
	// BackendURL is the core's encrypted HTTP state backend (STRATT_STATE_BACKEND_URL);
	// tofu points at it via TF_HTTP_ADDRESS. Empty runs against local state (dev only).
	BackendURL string
	// StateKeyHex derives the per-workspace TF_HTTP_PASSWORD (hex(HMAC(key, workspace)),
	// the statebackend scheme). The plugin holds it in ITS OWN config, never the core
	// (§2.5): the tofu-running process is the legitimate content-expert for state.
	StateKeyHex string
}

// workspaceCredential derives the per-workspace state credential the core's HTTP
// backend authorizes (hex(HMAC-SHA256(stateKey, workspace)) — the statebackend
// scheme, ADR-0016). "" when no key is configured (local-state dev path).
func (c Config) workspaceCredential(workspace string) string {
	if c.StateKeyHex == "" {
		return ""
	}
	key, err := hex.DecodeString(c.StateKeyHex)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(workspace))
	return hex.EncodeToString(mac.Sum(nil))
}
