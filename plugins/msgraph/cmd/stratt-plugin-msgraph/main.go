// Command stratt-plugin-msgraph serves the Microsoft Graph Syncer plugin over the
// sovereign plugin port (ADR-0046/0047). It is its own binary/build unit; the
// control plane connects to it over gRPC and governs what it may write — and, as
// the first DELTA-cursor plugin, persists the @odata.deltaLink cursor host-side.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/msgraph"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := msgraph.Config{
		PluginID:     env("STRATT_PLUGIN_ID", "msgraph"),
		Endpoint:     env("STRATT_MSGRAPH_ENDPOINT", "https://graph.microsoft.com/v1.0"),
		TenantID:     os.Getenv("STRATT_MSGRAPH_TENANT_ID"),
		ClientID:     os.Getenv("STRATT_MSGRAPH_CLIENT_ID"),
		ClientSecret: os.Getenv("STRATT_MSGRAPH_CLIENT_SECRET"),
		TokenURL:     os.Getenv("STRATT_MSGRAPH_TOKEN_URL"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, msgraph.NewServer(cfg, log))
	log.Info("msgraph plugin serving", "addr", addr, "endpoint", cfg.Endpoint, "plugin_id", cfg.PluginID)
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
