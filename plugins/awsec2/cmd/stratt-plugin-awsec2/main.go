// Command stratt-plugin-awsec2 serves the AWS EC2 Connector plugin over the
// sovereign plugin port (ADR-0046). It is its own binary/build unit: the control
// plane connects to it over gRPC and governs what it may write. It advertises both
// capabilities of the awsec2 Connector — the instance Syncer (Observe) and the
// create-vm Action (Invoke).
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/awsec2"
)

func main() {
	cfg := awsec2.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "awsec2"),
		Endpoint: os.Getenv("STRATT_AWSEC2_ENDPOINT"),
		Region:   pluginserve.Env("STRATT_AWSEC2_REGION", "us-east-1"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "awsec2",
		Server: awsec2.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"region", cfg.Region, "plugin_id", cfg.PluginID},
	})
}
