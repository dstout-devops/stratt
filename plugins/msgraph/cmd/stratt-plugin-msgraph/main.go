// Command stratt-plugin-msgraph serves the Microsoft Graph Syncer plugin over the
// sovereign plugin port (ADR-0046/0047). It is its own binary/build unit; the
// control plane connects to it over gRPC and governs what it may write — and, as
// the first DELTA-cursor plugin, persists the @odata.deltaLink cursor host-side.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/msgraph"
)

func main() {
	cfg := msgraph.Config{
		PluginID:     pluginserve.Env("STRATT_PLUGIN_ID", "msgraph"),
		Endpoint:     pluginserve.Env("STRATT_MSGRAPH_ENDPOINT", "https://graph.microsoft.com/v1.0"),
		TenantID:     os.Getenv("STRATT_MSGRAPH_TENANT_ID"),
		ClientID:     os.Getenv("STRATT_MSGRAPH_CLIENT_ID"),
		ClientSecret: os.Getenv("STRATT_MSGRAPH_CLIENT_SECRET"),
		TokenURL:     os.Getenv("STRATT_MSGRAPH_TOKEN_URL"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "msgraph",
		Server: msgraph.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"endpoint", cfg.Endpoint, "plugin_id", cfg.PluginID},
	})
}
