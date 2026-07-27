// Command stratt-plugin-vcenter serves the vCenter Syncer plugin over the
// sovereign plugin port (ADR-0046). It is its own binary/build unit; the control
// plane connects to it over gRPC and governs what it may write.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/vcenter"
)

func main() {
	cfg := vcenter.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "vcenter"),
		Endpoint: os.Getenv("STRATT_VCENTER_URL"),
		Username: pluginserve.Env("STRATT_VCENTER_USERNAME", "user"),
		Password: pluginserve.Env("STRATT_VCENTER_PASSWORD", "pass"),
		Insecure: os.Getenv("STRATT_VCENTER_INSECURE") == "true",
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "vcenter",
		Server: vcenter.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"endpoint", cfg.Endpoint, "plugin_id", cfg.PluginID},
	})
}
