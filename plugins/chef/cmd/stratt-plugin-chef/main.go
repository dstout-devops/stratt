// Command stratt-plugin-chef serves the Chef Syncer plugin over the sovereign
// plugin port (ADR-0046/0047). It is its own binary/build unit; the control
// plane connects to it over gRPC and governs what it may write.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/chef"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := chef.Config{
		PluginID:    env("STRATT_PLUGIN_ID", "chef"),
		ServerURL:   os.Getenv("STRATT_CHEF_SERVER_URL"),
		ClientName:  os.Getenv("STRATT_CHEF_CLIENT_NAME"),
		KeyPEM:      os.Getenv("STRATT_CHEF_KEY_PEM"),
		AuthVersion: os.Getenv("STRATT_CHEF_AUTH_VERSION"),
		SkipSSL:     os.Getenv("STRATT_CHEF_SKIP_SSL") == "true",
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, chef.NewServer(cfg, log))
	log.Info("chef plugin serving", "addr", addr, "endpoint", cfg.ServerURL, "plugin_id", cfg.PluginID)
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
