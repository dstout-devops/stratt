// Command stratt-plugin-opentofu serves the OpenTofu Actuator over the sovereign
// plugin port (ADR-0046/0047 slice 4): the Plan/Apply/Destroy converge verbs. Its
// own binary/build unit; the control plane connects over gRPC and governs what it
// may write, the target set, and Run provenance. tofu is a subprocess (§3).
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/opentofu"
)

func main() {
	cfg := opentofu.Config{
		PluginID:    pluginserve.Env("STRATT_PLUGIN_ID", "opentofu"),
		TofuBin:     pluginserve.Env("STRATT_TOFU_BIN", "tofu"),
		ModuleRoot:  pluginserve.Env("STRATT_TOFU_MODULE_ROOT", "/modules"),
		BackendURL:  os.Getenv("STRATT_STATE_BACKEND_URL"),
		StateKeyHex: os.Getenv("STRATT_STATE_KEY"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "opentofu",
		Server: opentofu.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"module_root", cfg.ModuleRoot, "plugin_id", cfg.PluginID},
	})
}
