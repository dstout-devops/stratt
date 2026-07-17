// Command stratt-ansible is the Ansible Actuator shim (ADR-0051): a ONE-SHOT binary
// that runs inside the EE image (a K8s Job, charter §3). It reads the request (params
// + core-resolved targets + dry-run) from the Job content, runs `ansible-runner` as a
// subprocess (the GPLv3 boundary), and emits the sovereign port's typed shapes as
// proto-JSON ApplyResponse lines on stdout. The core dispatches the Job, forwards the
// typed stream, and governs it hub-side (ApplyRaw) — this binary governs nothing.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dstout-devops/stratt/plugins/ansible"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stratt-ansible:", err)
		os.Exit(1)
	}
}

func run() error {
	reqPath := os.Getenv("STRATT_REQUEST")
	if reqPath == "" {
		reqPath = "/runner/stratt/request.json"
	}
	raw, err := os.ReadFile(reqPath)
	if err != nil {
		return fmt.Errorf("read request %s: %w", reqPath, err)
	}
	var req ansible.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	dir := os.Getenv("STRATT_RUNNER_DIR")
	if dir == "" {
		dir = "/runner"
	}
	bin := os.Getenv("STRATT_ANSIBLE_RUNNER")
	if bin == "" {
		bin = "ansible-runner"
	}
	return ansible.Execute(context.Background(), os.Stdout, dir, bin, req)
}
