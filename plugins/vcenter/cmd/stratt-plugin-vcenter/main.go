// Command stratt-plugin-vcenter serves the vCenter Syncer plugin over the
// sovereign plugin port (ADR-0046). It is its own binary/build unit; the control
// plane connects to it over gRPC and governs what it may write.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/vcenter"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := vcenter.Config{
		PluginID: env("STRATT_PLUGIN_ID", "vcenter"),
		Endpoint: os.Getenv("STRATT_VCENTER_URL"),
		Username: env("STRATT_VCENTER_USERNAME", "user"),
		Password: env("STRATT_VCENTER_PASSWORD", "pass"),
		Insecure: os.Getenv("STRATT_VCENTER_INSECURE") == "true",
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, vcenter.NewServer(cfg, log))
	log.Info("vcenter plugin serving", "addr", addr, "endpoint", cfg.Endpoint, "plugin_id", cfg.PluginID)
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
