// Command stratt-plugin-netbox serves the NetBox Syncer plugin over the sovereign
// plugin port (ADR-0046/0059). NetBox (netbox-community) is the network topology
// source of truth; the control plane dials this plugin over gRPC and governs what
// it may write. Its own build/CI unit; imports nothing from core/.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/netbox"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := netbox.Config{
		PluginID: env("STRATT_PLUGIN_ID", "netbox"),
		Endpoint: os.Getenv("STRATT_NETBOX_URL"),
		Token:    os.Getenv("STRATT_NETBOX_TOKEN"),
		Insecure: os.Getenv("STRATT_NETBOX_INSECURE") == "true",
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, netbox.NewServer(cfg, log))
	log.Info("netbox plugin serving", "addr", addr, "endpoint", cfg.Endpoint, "plugin_id", cfg.PluginID)
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
