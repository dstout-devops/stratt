// Command stratt-ansible is the Ansible Actuator shim (ADR-0051): a ONE-SHOT binary
// that runs inside the EE image (a K8s Job, charter §3). It reads the request — the
// SOVEREIGN port ApplyRequest (proto-JSON), the SAME shape the gRPC transport sends —
// from the Job content, runs `ansible-runner` as a subprocess (the GPLv3 boundary),
// and emits the port's typed shapes as proto-JSON ApplyResponse lines on stdout. The
// core dispatches the Job, forwards the typed stream, and governs it hub-side
// (GovernStream) — this binary governs nothing.
package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/plugins/ansible"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
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
	// The Job content is the sovereign ApplyRequest — the core encodes it, both
	// transports share it (ADR-0051): Desired is the opaque params (§1.1), Targets
	// are the LEGIBLE core-resolved set (MF4: name + vars + identity_keys for
	// write-back correlation), DryRun is the check-mode bit (MF6).
	var applyReq pluginv1.ApplyRequest
	if err := protojson.Unmarshal(raw, &applyReq); err != nil {
		return fmt.Errorf("decode ApplyRequest: %w", err)
	}
	req := ansible.Request{DryRun: applyReq.GetDryRun()}
	if d := applyReq.GetDesired(); d != nil {
		req.Params = d.GetBytes()
	}
	for _, t := range applyReq.GetTargets() {
		req.Targets = append(req.Targets, ansible.Target{
			Name: t.GetName(), Address: t.GetAddress(), Vars: t.GetVars(), Identity: t.GetIdentityKeys(),
		})
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
