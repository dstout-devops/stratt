// Command stratt-plugin-openbao serves the cert-issuer Connector plugin over
// the sovereign plugin port (ADR-0046). It is its own binary/build unit: the
// control plane connects to it over gRPC and governs what it may write. It
// advertises both capabilities of the cert-issuer Connector — the cert Syncer
// (Observe) and the issue/renew/revoke multi-op Action (Invoke). The CLM token is
// resolved from the environment at spawn (STRATT_OPENBAO_TOKEN) and never persisted
// (§2.5).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/openbao"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := openbao.Config{
		PluginID: env("STRATT_PLUGIN_ID", "openbao"),
		Addr:     env("STRATT_OPENBAO_ADDR", "http://localhost:8200"),
		Token:    os.Getenv("STRATT_OPENBAO_TOKEN"),
		Mount:    env("STRATT_OPENBAO_MOUNT", "pki"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, openbao.NewServer(cfg, log))
	log.Info("openbao plugin serving", "addr", addr, "openbao_addr", cfg.Addr, "plugin_id", cfg.PluginID)
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
