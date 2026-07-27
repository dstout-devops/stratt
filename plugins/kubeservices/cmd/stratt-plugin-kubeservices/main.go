// Command stratt-plugin-kubeservices serves the kubeservices Syncer over the
// sovereign plugin port (ADR-0046/0047, ADR-0081): it OBSERVEs Kubernetes Services
// and projects the service/capability dimension — `service` Entities, Helm-release
// `application` Entities, and the `provides` M:N edge. Its own binary/build unit; the
// control plane connects over gRPC and governs what it may write.
//
// The K8s client resolves in-cluster (a ServiceAccount, the deployed posture) and
// falls back to KUBECONFIG for local runs.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dstout-devops/stratt/plugins/kubeservices"
)

func main() {
	log := pluginserve.Logger()

	client, err := newClient()
	if err != nil {
		log.Error("kube client", "error", err)
		os.Exit(1)
	}
	cfg := kubeservices.Config{
		PluginID:      pluginserve.Env("STRATT_PLUGIN_ID", "kubeservices"),
		Namespace:     os.Getenv("STRATT_KUBESERVICES_NAMESPACE"), // "" = all namespaces
		ClusterDomain: os.Getenv("STRATT_KUBESERVICES_CLUSTER_DOMAIN"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "kubeservices",
		Server: kubeservices.NewServer(cfg, client, log),
		Fields: []any{"namespace", cfg.Namespace, "plugin_id", cfg.PluginID},
	})
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
