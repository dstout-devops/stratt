// Command stratt-notify serves the notification-delivery Action over the sovereign
// plugin port (ADR-0046/0052). It is its own binary/build unit: the control plane
// connects over gRPC and governs the invocation; this process resolves the Sink's
// per-call url/token via the SecretBroker (in-cluster K8s Secret reads under its OWN
// confined RBAC — MF-A) and issues the HTTP POST. Material never crosses the core.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dstout-devops/stratt/plugins/notify"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
)

func main() {
	log := pluginserve.Logger()

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
	ns := pluginserve.Env("STRATT_SECRET_NAMESPACE", os.Getenv("POD_NAMESPACE"))
	broker := secretbroker.New(cs, ns)

	pluginserve.Main(pluginserve.Config{
		Name:   "notify",
		Server: notify.New(pluginserve.Env("STRATT_PLUGIN_ID", "notify"), broker, log),
		Fields: []any{"secretNamespace", ns},
	})
}
