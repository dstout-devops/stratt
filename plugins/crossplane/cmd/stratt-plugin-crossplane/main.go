// Command stratt-plugin-crossplane serves the Crossplane build Actuator over the
// sovereign plugin port (ADR-0046/0059). Crossplane provisions infrastructure from
// Kubernetes Claims; the control plane dials this plugin as the `builder:` for
// network Intents and governs the write-back. Its own build/CI unit.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/crossplane"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := crossplane.Config{
		PluginID:   env("STRATT_PLUGIN_ID", "crossplane"),
		Kubeconfig: os.Getenv("STRATT_CROSSPLANE_KUBECONFIG"), // "" ⇒ in-cluster
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, crossplane.NewServer(cfg, log))
	log.Info("crossplane plugin serving", "addr", addr, "plugin_id", cfg.PluginID)
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
