// Command stratt-plugin-mesh serves the service-mesh dependency Syncer over the
// sovereign plugin port (ADR-0046/0047, ADR-0082 slice 2): it OBSERVEs a mesh's request
// telemetry and projects `service --depends-on--> service` edges. Its own binary/build
// unit; the control plane connects over gRPC and governs what it may write.
//
// The mesh flavor is not compiled in — it is the PromQL query + label names in the
// transport config (env below), so Istio/Linkerd/Consul/Cilium are configuration.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/mesh"
)

func main() {
	log := pluginserve.Logger()

	promEndpoint := os.Getenv("STRATT_MESH_PROMETHEUS_URL")
	if promEndpoint == "" {
		log.Error("STRATT_MESH_PROMETHEUS_URL is required (the mesh telemetry backend)")
		os.Exit(1)
	}
	src := mesh.NewPrometheusSource(mesh.PromConfig{
		Endpoint:  promEndpoint,
		Query:     os.Getenv("STRATT_MESH_QUERY"),      // "" ⇒ DefaultQuery (Istio)
		FromLabel: os.Getenv("STRATT_MESH_FROM_LABEL"), // "" ⇒ source_fqdn
		ToLabel:   os.Getenv("STRATT_MESH_TO_LABEL"),   // "" ⇒ destination_fqdn
	}, nil)

	cfg := mesh.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "mesh"),
		// Opt in only for a mesh legitimately expected to reach zero edges; by default
		// an empty snapshot is treated as a likely misconfiguration and holds steady.
		AllowEmptyFullSync: os.Getenv("STRATT_MESH_ALLOW_EMPTY_FULL_SYNC") == "true",
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "mesh",
		Server: mesh.NewServer(cfg, src, log),
		Fields: []any{"plugin_id", cfg.PluginID, "prometheus", promEndpoint},
	})
}
