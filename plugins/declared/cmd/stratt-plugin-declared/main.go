// Command stratt-plugin-declared serves the declared-estate Syncer plugin over
// the sovereign plugin port (ADR-0046/0056). Its system-of-record is a directory
// of host-list files delivered with the estate (the same CaC checkout the control
// plane reconciles). It is its own binary/build unit; the control plane connects
// over gRPC and governs what it may write.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/declared"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := declared.Config{
		PluginID: env("STRATT_PLUGIN_ID", "declared"),
		// The host-list directory. Defaults to the estate's hosts/ under the
		// reconciled desired-state checkout mounted into this pod.
		Path: env("STRATT_DECLARED_PATH", "/declarations/hosts"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, declared.NewServer(cfg, log))
	log.Info("declared plugin serving", "addr", addr, "path", cfg.Path, "plugin_id", cfg.PluginID)
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
