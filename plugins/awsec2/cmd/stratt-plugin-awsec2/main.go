// Command stratt-plugin-awsec2 serves the AWS EC2 Connector plugin over the
// sovereign plugin port (ADR-0046). It is its own binary/build unit: the control
// plane connects to it over gRPC and governs what it may write. It advertises both
// capabilities of the awsec2 Connector — the instance Syncer (Observe) and the
// create-vm Action (Invoke).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/awsec2"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := awsec2.Config{
		PluginID: env("STRATT_PLUGIN_ID", "awsec2"),
		Endpoint: os.Getenv("STRATT_AWSEC2_ENDPOINT"),
		Region:   env("STRATT_AWSEC2_REGION", "us-east-1"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, awsec2.NewServer(cfg, log))
	log.Info("awsec2 plugin serving", "addr", addr, "region", cfg.Region, "plugin_id", cfg.PluginID)
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
