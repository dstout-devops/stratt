// Command stratt-plugin-puppet serves the OpenVox/PuppetDB Syncer plugin over the
// sovereign plugin port (ADR-0046/0047). It is its own binary/build unit; the
// control plane connects to it over gRPC and governs what it may write.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/puppet"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := puppet.Config{
		PluginID: env("STRATT_PLUGIN_ID", "puppet"),
		BaseURL:  os.Getenv("STRATT_PUPPETDB_URL"),
		CertFile: os.Getenv("STRATT_PUPPETDB_CERT"),
		KeyFile:  os.Getenv("STRATT_PUPPETDB_KEY"),
		CAFile:   os.Getenv("STRATT_PUPPETDB_CA"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, puppet.NewServer(cfg, log))
	log.Info("puppet plugin serving", "addr", addr, "endpoint", cfg.BaseURL, "plugin_id", cfg.PluginID)
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
