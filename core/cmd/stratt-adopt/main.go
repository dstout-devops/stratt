// Command stratt-adopt serves the adopt/materialize Action over the sovereign plugin port
// (ADR-0088). It is a CORE-OWNED Action server (it runs the core awximport transform), so it
// lives in the core module and ships as a core-owned image — distinct from the SDK-only
// dark-matter plugins. The control plane connects over gRPC and governs the invocation; this
// process resolves the AWX CredentialRef via the SecretBroker (in-cluster K8s Secret reads
// under its OWN confined RBAC), does the targeted deep-read + transform, and returns the
// reviewable bundle. AWX material never crosses the core (§2.5).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dstout-devops/stratt/core/internal/adoptplugin"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// In-cluster K8s client for the SecretBroker resolver. The pod's ServiceAccount RBAC is
	// confined to its brokerable AWX Secret — it can read no other Secret, so the RBAC gate
	// ≈ the per-call use-grant the core already enforced (the notify MF-A pattern).
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
	pluginv1.RegisterPluginServiceServer(srv, adoptplugin.New(env("STRATT_PLUGIN_ID", "adopt"), broker, log))
	log.Info("adopt plugin serving", "addr", addr, "secretNamespace", ns)
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
