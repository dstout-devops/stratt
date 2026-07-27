// Command stratt-plugin-chef serves the Chef Syncer plugin over the sovereign
// plugin port (ADR-0046/0047). It is its own binary/build unit; the control
// plane connects to it over gRPC and governs what it may write.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/chef"
)

func main() {
	cfg := chef.Config{
		PluginID:    pluginserve.Env("STRATT_PLUGIN_ID", "chef"),
		ServerURL:   os.Getenv("STRATT_CHEF_SERVER_URL"),
		ClientName:  os.Getenv("STRATT_CHEF_CLIENT_NAME"),
		KeyPEM:      os.Getenv("STRATT_CHEF_KEY_PEM"),
		AuthVersion: os.Getenv("STRATT_CHEF_AUTH_VERSION"),
		SkipSSL:     os.Getenv("STRATT_CHEF_SKIP_SSL") == "true",
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "chef",
		Server: chef.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"endpoint", cfg.ServerURL, "plugin_id", cfg.PluginID},
	})
}
