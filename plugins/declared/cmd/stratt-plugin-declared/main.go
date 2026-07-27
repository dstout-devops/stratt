// Command stratt-plugin-declared serves the declared-estate Syncer plugin over
// the sovereign plugin port (ADR-0046/0056). Its system-of-record is a directory
// of host-list files delivered with the estate (the same CaC checkout the control
// plane reconciles). It is its own binary/build unit; the control plane connects
// over gRPC and governs what it may write.
package main

import (
	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/declared"
)

func main() {
	cfg := declared.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "declared"),
		// The host-list directory. Defaults to the estate's hosts/ under the
		// reconciled desired-state checkout mounted into this pod.
		Path: pluginserve.Env("STRATT_DECLARED_PATH", "/declarations/hosts"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "declared",
		Server: declared.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"path", cfg.Path, "plugin_id", cfg.PluginID},
	})
}
