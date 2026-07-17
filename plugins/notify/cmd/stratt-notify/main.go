// Command stratt-notify serves the notification-delivery Action over the sovereign
// plugin port (ADR-0046/0052). It is its own binary/build unit: the control plane
// connects over gRPC and governs the invocation; this process resolves the Sink's
// per-call url/token via the SecretBroker (in-cluster K8s Secret reads under its OWN
// confined RBAC — MF-A) and issues the HTTP POST. Material never crosses the core.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dstout-devops/stratt/plugins/notify"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// In-cluster K8s client for the SecretBroker resolver. The plugin's ServiceAccount
	// RBAC is confined to its brokerable Secrets (MF-A) — it can read no other Secret,
	// so the RBAC gate ≈ the per-call use-grant the core already enforced.
	rc, err := rest.InClusterConfig()
	if err != nil {
		log.Error("in-cluster config", "error", err)
		os.Exit(1)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		log.Error("kubernetes client", "error", err)
		os.Exit(1)
	}
	ns := env("STRATT_SECRET_NAMESPACE", os.Getenv("POD_NAMESPACE"))
	broker := secretbroker.New(cs, ns)

	addr := env("STRATT_PLUGIN_LISTEN", ":9090")
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, notify.New(env("STRATT_PLUGIN_ID", "notify"), broker, log))
	log.Info("notify plugin serving", "addr", addr, "secretNamespace", ns)
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
