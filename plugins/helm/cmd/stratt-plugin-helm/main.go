// Command stratt-plugin-helm serves the Helm Actuator over the sovereign plugin
// port (ADR-0092): the Plan (helm template) and Apply (helm upgrade --install)
// verbs. Its own binary/build unit; the control plane connects over gRPC and governs
// the target set and Run provenance. helm is a subprocess (§3); kube access comes
// from the pod's in-cluster ServiceAccount (per-route scoped, ADR-0092 §6).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/helm"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := helm.Config{
		PluginID:  env("STRATT_PLUGIN_ID", "helm"),
		HelmBin:   env("STRATT_HELM_BIN", "helm"),
		ChartRoot: os.Getenv("STRATT_HELM_CHART_ROOT"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, helm.NewServer(cfg, log))
	log.Info("helm plugin serving", "addr", addr, "plugin_id", cfg.PluginID, "chart_root", cfg.ChartRoot)
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
