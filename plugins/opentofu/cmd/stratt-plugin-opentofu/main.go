// Command stratt-plugin-opentofu serves the OpenTofu Actuator over the sovereign
// plugin port (ADR-0046/0047 slice 4): the Plan/Apply/Destroy converge verbs. Its
// own binary/build unit; the control plane connects over gRPC and governs what it
// may write, the target set, and Run provenance. tofu is a subprocess (§3).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/opentofu"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := opentofu.Config{
		PluginID:    env("STRATT_PLUGIN_ID", "opentofu"),
		TofuBin:     env("STRATT_TOFU_BIN", "tofu"),
		ModuleRoot:  env("STRATT_TOFU_MODULE_ROOT", "/modules"),
		BackendURL:  os.Getenv("STRATT_STATE_BACKEND_URL"),
		StateKeyHex: os.Getenv("STRATT_STATE_KEY"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, opentofu.NewServer(cfg, log))
	log.Info("opentofu plugin serving", "addr", addr, "module_root", cfg.ModuleRoot, "plugin_id", cfg.PluginID)
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
