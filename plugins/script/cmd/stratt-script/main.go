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
	"os"

	"github.com/dstout-devops/stratt/plugins/script"
	"github.com/dstout-devops/stratt/sdk/pluginserve"
)

func main() { pluginserve.JobMain("stratt-script", run) }

func run() error {
	applyReq, err := pluginserve.ReadRequest()
	if err != nil {
		return err
	}
	req := script.Request{}
	if d := applyReq.GetDesired(); d != nil {
		req.Params = d.GetBytes()
	}
	for _, t := range applyReq.GetTargets() {
		req.Targets = append(req.Targets, script.Target{Name: t.GetName(), Vars: t.GetVars()})
	}
	dir := pluginserve.RunnerDir("/runner/project")
	return script.Execute(context.Background(), os.Stdout, dir, req)
}
