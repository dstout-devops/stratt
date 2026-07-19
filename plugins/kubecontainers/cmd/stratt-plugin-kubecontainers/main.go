// Command stratt-plugin-kubecontainers serves the container-image Syncer over the
// sovereign plugin port (ADR-0046/0047, ADR-0080): it OBSERVEs running Pods and projects
// the `software.container` inventory onto the nodes that run them, feeding the one
// form-agnostic software-advisory check. Its own binary/build unit; the control plane
// connects over gRPC and governs what it may write.
//
// The K8s client resolves in-cluster (a ServiceAccount, the deployed posture) and falls
// back to KUBECONFIG for local runs.
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dstout-devops/stratt/plugins/kubecontainers"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	client, err := newClient()
	if err != nil {
		log.Error("kube client", "error", err)
		os.Exit(1)
	}
	cfg := kubecontainers.ServerConfig{
		PluginID:  env("STRATT_PLUGIN_ID", "kubecontainers"),
		Namespace: os.Getenv("STRATT_KUBECONTAINERS_NAMESPACE"), // "" = all namespaces
		Config: kubecontainers.Config{
			// Cluster qualifier for the globally-unique node identity; "" ⇒ auto-resolve
			// from the kube-system namespace UID.
			ClusterID: os.Getenv("STRATT_KUBECONTAINERS_CLUSTER_ID"),
			// Images from this registry prefix are tagged origin "internal"; others get
			// no origin (lineage is never guessed).
			InternalRegistry: os.Getenv("STRATT_KUBECONTAINERS_INTERNAL_REGISTRY"),
		},
		// Opt in only for a cluster legitimately expected to be empty; by default an empty
		// pod snapshot is treated as a likely degraded read and holds steady.
		AllowEmptyFullSync: os.Getenv("STRATT_KUBECONTAINERS_ALLOW_EMPTY_FULL_SYNC") == "true",
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, kubecontainers.NewServer(cfg, client, log))
	log.Info("kubecontainers plugin serving", "addr", addr, "namespace", cfg.Namespace, "plugin_id", cfg.PluginID)
	if err := srv.Serve(lis); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

// newClient resolves the Kubernetes client: in-cluster first (the deployed
// ServiceAccount), then KUBECONFIG / ~/.kube/config for local runs.
func newClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
