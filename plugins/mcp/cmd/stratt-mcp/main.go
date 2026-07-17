// Command stratt-mcp is the MCP-client shim (ADR-0053): a ONE-SHOT binary that runs
// inside the EE image (a K8s Job, charter §3, §7.3 sandbox). It reads the sovereign
// ApplyRequest (proto-JSON) from the Job content, speaks JSON-RPC to the declared MCP
// server (stdio subprocess / HTTP), and emits the port's typed shapes on stdout. The
// core dispatches the Job, forwards the typed stream, and pins/governs it hub-side.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dstout-devops/stratt/plugins/mcp"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stratt-mcp:", err)
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
	var step mcp.Step
	if d := applyReq.GetDesired(); d != nil && len(d.GetBytes()) > 0 {
		if err := json.Unmarshal(d.GetBytes(), &step); err != nil {
			return fmt.Errorf("decode mcp step: %w", err)
		}
	}
	dir := os.Getenv("STRATT_RUNNER_DIR")
	if dir == "" {
		dir = "/runner/project"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return mcp.Execute(context.Background(), os.Stdout, dir, step)
}
