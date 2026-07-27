// Command stratt-plugin-netbox serves the NetBox Syncer plugin over the sovereign
// plugin port (ADR-0046/0059). NetBox (netbox-community) is the network topology
// source of truth; the control plane dials this plugin over gRPC and governs what
// it may write. Its own build/CI unit; imports nothing from core/.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/plugins/netbox"
	"github.com/dstout-devops/stratt/sdk/pluginserve"
)

func main() {
	cfg := netbox.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "netbox"),
		Endpoint: os.Getenv("STRATT_NETBOX_URL"),
		Token:    os.Getenv("STRATT_NETBOX_TOKEN"),
		Insecure: os.Getenv("STRATT_NETBOX_INSECURE") == "true",
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "netbox",
		Server: netbox.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"endpoint", cfg.Endpoint, "plugin_id", cfg.PluginID},
	})
}
