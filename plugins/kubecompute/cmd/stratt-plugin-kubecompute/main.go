// Command stratt-plugin-kubecompute serves the container-image Syncer over the
// sovereign plugin port (ADR-0046/0047, ADR-0080): it OBSERVEs running Pods and projects
// the `software.container` inventory onto the nodes that run them, feeding the one
// form-agnostic software-advisory check. Its own binary/build unit; the control plane
// connects over gRPC and governs what it may write.
//
// The K8s client resolves in-cluster (a ServiceAccount, the deployed posture) and falls
// back to KUBECONFIG for local runs.
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dstout-devops/stratt/plugins/kubecompute"
)

func main() {
	log := pluginserve.Logger()

	client, err := newClient()
	if err != nil {
		log.Error("kube client", "error", err)
		os.Exit(1)
	}
	cfg := kubecompute.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "kubecompute"),
		// The namespace hosts are BUILT into. Defaulted rather than left empty: an empty
		// namespace means "all namespaces" to a List and is meaningless to a Create, so a
		// builder with no namespace would observe everything and build nowhere.
		Namespace: pluginserve.Env("STRATT_KUBECOMPUTE_NAMESPACE", "stratt"),
		// The built host's base image. It must carry a shell and apk; the build installs
		// openssh + python3 into it, which is what makes the result converge-able.
		Image: pluginserve.Env("STRATT_KUBECOMPUTE_IMAGE", "alpine:3.22"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "kubecompute",
		Server: kubecompute.NewServer(cfg, client, log),
		Fields: []any{"namespace", cfg.Namespace, "image", cfg.Image, "plugin_id", cfg.PluginID},
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
