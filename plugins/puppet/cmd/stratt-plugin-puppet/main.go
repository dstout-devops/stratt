// Command stratt-plugin-puppet serves the OpenVox/PuppetDB Syncer plugin over the
// sovereign plugin port (ADR-0046/0047). It is its own binary/build unit; the
// control plane connects to it over gRPC and governs what it may write.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/puppet"
)

func main() {
	cfg := puppet.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "puppet"),
		BaseURL:  os.Getenv("STRATT_PUPPETDB_URL"),
		CertFile: os.Getenv("STRATT_PUPPETDB_CERT"),
		KeyFile:  os.Getenv("STRATT_PUPPETDB_KEY"),
		CAFile:   os.Getenv("STRATT_PUPPETDB_CA"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "puppet",
		Server: puppet.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"endpoint", cfg.BaseURL, "plugin_id", cfg.PluginID},
	})
}
