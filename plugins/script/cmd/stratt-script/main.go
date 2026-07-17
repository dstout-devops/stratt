// Command stratt-script is the script Actuator shim (ADR-0046/0051): a ONE-SHOT
// binary that runs inside the EE image (a K8s Job, charter §3). It reads the request
// — the SOVEREIGN port ApplyRequest (proto-JSON), the SAME shape the gRPC transport
// sends — from the Job content, runs the user's script once per core-resolved target
// (sh / python3 subprocess — the GPL/tooling boundary), and emits the port's typed
// shapes as proto-JSON ApplyResponse lines on stdout. The core dispatches the Job,
// forwards the typed stream, and governs it hub-side (GovernStream).
package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/plugins/script"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stratt-script:", err)
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
	var applyReq pluginv1.ApplyRequest
	if err := protojson.Unmarshal(raw, &applyReq); err != nil {
		return fmt.Errorf("decode ApplyRequest: %w", err)
	}
	req := script.Request{}
	if d := applyReq.GetDesired(); d != nil {
		req.Params = d.GetBytes()
	}
	for _, t := range applyReq.GetTargets() {
		req.Targets = append(req.Targets, script.Target{Name: t.GetName(), Vars: t.GetVars()})
	}
	dir := os.Getenv("STRATT_RUNNER_DIR")
	if dir == "" {
		dir = "/runner/project"
	}
	return script.Execute(context.Background(), os.Stdout, dir, req)
}
