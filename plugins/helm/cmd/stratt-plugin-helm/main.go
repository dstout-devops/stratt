// Command stratt-plugin-helm serves the Helm Actuator over the sovereign plugin
// port (ADR-0092): the Plan (helm template) and Apply (helm upgrade --install)
// verbs. Its own binary/build unit; the control plane connects over gRPC and governs
// the target set and Run provenance. helm is a subprocess (§3); kube access comes
// from the pod's in-cluster ServiceAccount (per-route scoped, ADR-0092 §6).
package main

import (
	"os"

	"github.com/dstout-devops/stratt/plugins/helm"
	"github.com/dstout-devops/stratt/sdk/pluginserve"
)

func main() {
	cfg := helm.Config{
		PluginID:  pluginserve.Env("STRATT_PLUGIN_ID", "helm"),
		HelmBin:   pluginserve.Env("STRATT_HELM_BIN", "helm"),
		ChartRoot: os.Getenv("STRATT_HELM_CHART_ROOT"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "helm",
		Server: helm.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"plugin_id", cfg.PluginID, "chart_root", cfg.ChartRoot},
	})
}
