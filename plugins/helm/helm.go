package helm

import (
	"bufio"
	"context"
	"os/exec"
)

// commandRunner runs one helm invocation in dir, streaming each stdout line to
// onLine, and returns the full stdout plus the process exit code. Injectable so
// tests drive canned helm output with NO helm binary — helm is a SUBPROCESS, never
// linked (charter §3; §1.5 transports beneath the contract).
type commandRunner interface {
	run(ctx context.Context, dir string, env, args []string, onLine func([]byte)) (stdout []byte, rc int, err error)
}

// execRunner shells out to the real `helm` binary.
type execRunner struct{ bin string }

func (e execRunner) run(ctx context.Context, dir string, env, args []string, onLine func([]byte)) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, -1, err
	}
	cmd.Stderr = cmd.Stdout // helm errors ride the same stream — never dropped (§1.8)
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	var full []byte
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 32<<20) // rendered manifests can be large
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		full = append(append(full, line...), '\n')
		if onLine != nil {
			onLine(line)
		}
	}
	// A non-zero exit is a helm RESULT (rc), not a runner error — the caller folds it
	// into the release ItemResult; only a spawn/pipe failure is a runner error.
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
	// HelmBin is the helm executable (default "helm"). Pinned to the Helm 4 line in
	// the image (ADR-0092; dependency-scout v4.2.3).
	HelmBin string
	// ChartRoot is the base directory for LOCAL chart references (a params.Chart that
	// is not an oci:// ref or a repo chart resolves under it). Empty ⇒ chart refs are
	// taken as-is (OCI / repo / absolute path).
	ChartRoot string
	// HelmHome is a WRITABLE base for helm's cache/config/data (the pod rootfs is
	// read-only, §7.3; the chart mounts an emptyDir here). Empty ⇒ "/tmp/helm".
	HelmHome string
}
