// Command stratt-plugin-mesh serves the service-mesh dependency Syncer over the
// sovereign plugin port (ADR-0046/0047, ADR-0082 slice 2): it OBSERVEs a mesh's request
// telemetry and projects `service --depends-on--> service` edges. Its own binary/build
// unit; the control plane connects over gRPC and governs what it may write.
//
// The mesh flavor is not compiled in — it is the PromQL query + label names in the
// transport config (env below), so Istio/Linkerd/Consul/Cilium are configuration.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/mesh"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

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
		PluginID: env("STRATT_PLUGIN_ID", "mesh"),
		// Opt in only for a mesh legitimately expected to reach zero edges; by default
		// an empty snapshot is treated as a likely misconfiguration and holds steady.
		AllowEmptyFullSync: os.Getenv("STRATT_MESH_ALLOW_EMPTY_FULL_SYNC") == "true",
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, mesh.NewServer(cfg, src, log))
	log.Info("mesh plugin serving", "addr", addr, "plugin_id", cfg.PluginID, "prometheus", promEndpoint)
	if err := srv.Serve(lis); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
