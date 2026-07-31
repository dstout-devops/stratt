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
	"os"

	"github.com/dstout-devops/stratt/plugins/ansible"
	"github.com/dstout-devops/stratt/sdk/pluginserve"
)

func main() { pluginserve.JobMain("stratt-ansible", run) }

func run() error {
	applyReq, err := pluginserve.ReadRequest()
	if err != nil {
		return err
	}
	req := ansible.Request{DryRun: applyReq.GetDryRun()}
	if d := applyReq.GetDesired(); d != nil {
		req.Params = d.GetBytes()
	}
	for _, t := range applyReq.GetTargets() {
		hops := make([]ansible.Hop, 0, len(t.GetJump()))
		for _, h := range t.GetJump() {
			hops = append(hops, ansible.Hop{Name: h.GetName(), Address: h.GetAddress(), Port: h.GetPort()})
		}
		at := ansible.Target{
			Name: t.GetName(), Address: t.GetAddress(), Port: t.GetPort(),
			Vars: t.GetVars(), Identity: t.GetIdentityKeys(), Jump: hops,
		}
		// The OBSERVED transport (ADR-0156): kind is legible, coordinates are opaque to core
		// and read by the shim. Absent ⇒ nothing observed and ansible's default applies.
		if tr := t.GetTransport(); tr != nil {
			at.TransportKind, at.TransportCoordinates = tr.GetKind(), tr.GetCoordinates()
		}
		req.Targets = append(req.Targets, at)
	}

	dir := pluginserve.RunnerDir("/runner")
	bin := os.Getenv("STRATT_ANSIBLE_RUNNER")
	if bin == "" {
		bin = "ansible-runner"
	}
	return ansible.Execute(context.Background(), os.Stdout, dir, bin, req)
}
