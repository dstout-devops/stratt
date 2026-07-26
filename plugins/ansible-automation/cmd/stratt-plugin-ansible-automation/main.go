// Command stratt-plugin-ansible-automation serves the ansible-automation Connector over
// the sovereign plugin port (ADR-0046/0047/0127). ONE binary, ONE image, ONE plugin
// identity — and TWO halves of the Ansible Automation Platform surface, selected per
// instance by STRATT_ANSIBLE_AUTOMATION_ROLE:
//
//	controller — OBSERVEs an AAP Controller's /api/v2 (job templates, workflows,
//	             schedules, orgs, teams) and provides the adopt/materialize Action.
//	content    — OBSERVEs a raw Ansible content root (a mounted Git checkout of
//	             playbooks, roles, requirements.yml, inventory). "Ansible without AWX".
//
// ONE INSTANCE PER SOURCE, and the role selector is what makes that honest. The port
// carries no grant discriminator — GetManifest returns one Manifest per address and
// pluginhost.Register rejects every advertised contract outside the dialing grant's
// FacetNamespaces — so an address serving both halves could register as neither. Nor
// could one address ever serve two Controllers: endpoint, credential, and content root
// are instance config. So the deployment rule is the one vcenter (per vCenter) and netbox
// (per NetBox) already follow; the role selector just names which half this instance is.
//
// The selector is also the least-privilege boundary (§2.5): only role=controller
// constructs the SecretBroker, so a content-only install grants no Secret access at all
// and reaches no Controller code path.
//
// Read-only either way (§1.2): AAP and Git stay authoritative and AAP keeps executing;
// we never import — the projection is always-on and `stratt adopt` is the deliberate act
// that takes authority over an already-observed object. Its own binary/build/CI unit
// (module isolation, ADR-0046).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dstout-devops/stratt/plugins/ansible-automation/content"
	"github.com/dstout-devops/stratt/plugins/ansible-automation/controller"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// pluginID is the one identity BOTH halves assert — one plugin, two Grants (ADR-0127 D1).
// The grant is keyed on it (anti-spoof), and both strattd boot blocks grant this same name.
const pluginID = "ansible-automation"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	role := os.Getenv("STRATT_ANSIBLE_AUTOMATION_ROLE")
	var srv *grpc.Server
	switch role {
	case "controller":
		srv = serveController(log)
	case "content":
		srv = serveContent(log)
	case "":
		log.Error("STRATT_ANSIBLE_AUTOMATION_ROLE is required (controller|content) — one instance per Source (ADR-0127 D1)")
		os.Exit(1)
	default:
		log.Error("unknown STRATT_ANSIBLE_AUTOMATION_ROLE (want controller|content)", "role", role)
		os.Exit(1)
	}

	addr := env("STRATT_PLUGIN_LISTEN", ":9090")
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	log.Info("ansible-automation Connector serving", "addr", addr, "role", role, "plugin_id", pluginID)
	if err := srv.Serve(lis); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

// serveController builds the AAP Controller half: the /api/v2 projection plus the
// adopt/materialize Action. Bound to exactly ONE Controller (endpoint + token) — which is
// why the instance, not the process, is what maps to a Source.
func serveController(log *slog.Logger) *grpc.Server {
	endpoint := os.Getenv("STRATT_ANSIBLE_CONTROLLER_ENDPOINT")
	if endpoint == "" {
		log.Error("STRATT_ANSIBLE_CONTROLLER_ENDPOINT is required for role=controller (the AAP Controller base URL, e.g. https://aap.example.com)")
		os.Exit(1)
	}
	client := controller.New(controller.Config{
		Endpoint:     endpoint,
		Token:        os.Getenv("STRATT_ANSIBLE_CONTROLLER_TOKEN"),
		ControllerID: os.Getenv("STRATT_ANSIBLE_CONTROLLER_ID"), // "" ⇒ the endpoint host
	})

	// The SecretBroker backs the adopt/materialize Action ONLY (§2.5): it resolves the
	// Controller CredentialRef in-pod under this plugin's confined RBAC. Best-effort — a
	// Syncer-only deployment (or local dev) with no in-cluster config runs fine with
	// broker=nil; the Action then fails closed. The Syncer path never touches it, and
	// role=content never reaches this function at all.
	var broker *secretbroker.Resolver
	if rc, err := rest.InClusterConfig(); err == nil {
		if cs, cerr := kubernetes.NewForConfig(rc); cerr == nil {
			ns := env("STRATT_SECRET_NAMESPACE", os.Getenv("POD_NAMESPACE"))
			broker = secretbroker.New(cs, ns)
			log.Info("adopt SecretBroker ready", "secretNamespace", ns)
		} else {
			log.Warn("k8s client for SecretBroker unavailable; adopt/materialize will fail closed", "error", cerr)
		}
	} else {
		log.Info("no in-cluster config; adopt/materialize disabled (Syncer-only)", "reason", err)
	}

	cfg := controller.ServerConfig{
		PluginID:           env("STRATT_PLUGIN_ID", pluginID),
		AllowEmptyFullSync: os.Getenv("STRATT_ANSIBLE_CONTROLLER_ALLOW_EMPTY_FULL_SYNC") == "true",
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, controller.NewServer(cfg, client, broker, log))
	log.Info("role=controller", "endpoint", endpoint)
	return srv
}

// serveContent builds the content-root half. Bound to exactly ONE content root — again,
// the instance is the Source. No credential, no network egress, no Secret access.
func serveContent(log *slog.Logger) *grpc.Server {
	root := os.Getenv("STRATT_ANSIBLE_CONTENT_ROOT")
	if root == "" {
		log.Error("STRATT_ANSIBLE_CONTENT_ROOT is required for role=content (the Ansible content root — a mounted Git checkout / directory)")
		os.Exit(1)
	}
	client := content.New(content.Config{
		Root:      root,
		ProjectID: os.Getenv("STRATT_ANSIBLE_CONTENT_ID"), // "" ⇒ base name of the root
	})
	cfg := content.ServerConfig{
		PluginID:           env("STRATT_PLUGIN_ID", pluginID),
		AllowEmptyFullSync: os.Getenv("STRATT_ANSIBLE_CONTENT_ALLOW_EMPTY_FULL_SYNC") == "true",
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, content.NewServer(cfg, client, log))
	log.Info("role=content", "root", root)
	return srv
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
