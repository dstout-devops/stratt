// Command stratt-plugin-crossplane serves the Crossplane build Actuator over the
// sovereign plugin port (ADR-0046/0059). Crossplane provisions infrastructure from
// Kubernetes Claims; the control plane dials this plugin as the `builder:` for
// network Intents and governs the write-back. Its own build/CI unit.
package main

import (
	"encoding/json"
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/crossplane"
)

func main() {
	log := pluginserve.Logger()
	cfg := crossplane.Config{
		PluginID:   pluginserve.Env("STRATT_PLUGIN_ID", "crossplane"),
		Kubeconfig: os.Getenv("STRATT_CROSSPLANE_KUBECONFIG"), // "" ⇒ in-cluster
	}
	// STRATT_CROSSPLANE_OBSERVE is a JSON array of Claim kinds to observe back as a
	// registered Source (the SYNCER verb). Empty ⇒ build-only (Observe streams empty).
	if raw := os.Getenv("STRATT_CROSSPLANE_OBSERVE"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.ObserveClaims); err != nil {
			log.Error("parse STRATT_CROSSPLANE_OBSERVE", "error", err)
			os.Exit(1)
		}
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "crossplane",
		Server: crossplane.NewServer(cfg, log),
		Fields: []any{"plugin_id", cfg.PluginID},
	})
}
