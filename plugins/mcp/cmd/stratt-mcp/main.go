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

	"github.com/dstout-devops/stratt/plugins/mcp"
	"github.com/dstout-devops/stratt/sdk/pluginserve"
)

func main() { pluginserve.JobMain("stratt-mcp", run) }

func run() error {
	applyReq, err := pluginserve.ReadRequest()
	if err != nil {
		return err
	}
	var step mcp.Step
	if d := applyReq.GetDesired(); d != nil && len(d.GetBytes()) > 0 {
		if err := json.Unmarshal(d.GetBytes(), &step); err != nil {
			return fmt.Errorf("decode mcp step: %w", err)
		}
	}
	dir := pluginserve.RunnerDir("/runner/project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return mcp.Execute(context.Background(), os.Stdout, dir, step)
}
