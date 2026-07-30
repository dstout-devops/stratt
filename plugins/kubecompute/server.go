// Package kubecompute is the `provisioning` capability's provider for Intent/Compute on
// Kubernetes (ADR-0151): it BUILDS a host that can actually be converged, and projects how to
// reach it.
//
// The gap it closes is the capstone's middle. A floci instance is a real machine record with
// nothing listening, and vcenter/vcsim is `build-real`, so provision-then-configure could never
// run as one act — the app and certificate legs were proven against a statically-declared
// Deployment. A host built here runs sshd and python3, so the ansible reach path (ADR-0084)
// converges it with no special case.
package kubecompute

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const (
	// ActionCreateHost builds one host. Named for what it makes, not for how: nothing in the
	// estate above it should have to know this provider renders a Pod (§1.5).
	ActionCreateHost = "kubecompute/create-host"
	// SchemeHost is the identity a built host is known by — namespace/name, which is stable
	// across the pod's lifetime and is what a rebuild would reuse. NOT the pod UID: a UID
	// changes when the pod is replaced, and the Entity would fork rather than be updated.
	SchemeHost = "kube.host"
	// managedLabel marks what THIS plugin built, and bounds its Observe to exactly that. The
	// cluster is full of pods it did not build and must not claim (§2.1) — kubecontainers
	// projects container inventory per node and would collide with a broader scope here.
	managedLabel = "stratt.io/kubecompute"
	// kindLabel records the Entity kind the build was asked to project, so Observe and the build
	// cannot disagree about it.
	kindLabel = "stratt.io/project-kind"
	// hostLabel is the per-host selector the built Service targets.
	hostLabel = "stratt.io/host"
)

// Config locates the build scope and the host image.
type Config struct {
	PluginID  string
	Namespace string
	// Image is the built host's base. It must carry a shell and a package manager; the build
	// installs openssh + python3 into it, which is what makes the result converge-able rather
	// than merely running.
	Image string
}

// Server implements the sovereign port for kubecompute.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg    Config
	client kubernetes.Interface
	log    *slog.Logger
}

// NewServer wires the plugin over a Kubernetes client.
//
// No poll-interval knob: the CORE drives the Observe cadence for every Syncer, and a second
// interval here would be a value that can disagree with the one actually in force.
func NewServer(cfg Config, client kubernetes.Interface, log *slog.Logger) *Server {
	return &Server{cfg: cfg, client: client, log: log}
}

// GetManifest advertises what this plugin can actually back.
func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		// Dual-verb, the netbox/crossplane precedent (ADR-0111 D4): Class is the advisory primary
		// kind, Verbs are the authoritative surface the host gates on. This plugin OBSERVES what it
		// built and INVOKES to build more.
		Class:       pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:       []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		ObserveMode: pluginv1.Manifest_OBSERVE_MODE_POLL,
		// mgmt.address is the reach coordinate the shim renders into ansible_host (ADR-0084). It is
		// the ONE Facet this plugin owns, and owning it is the point: a builder that cannot say how
		// to reach what it built has not finished the job.
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "mgmt.address"},
		},
		// Tombstone by the host scheme: a built host that is gone from the API is gone. Union
		// liveness (ADR-0042) keeps the Entity alive if another Source still observes it.
		TombstoneSchemes: []string{SchemeHost},
		// `provisioning` is advertised UNCONDITIONALLY, unlike a capability that needs
		// configuration to be real (the ADR-0106 D2 honesty rule): building a pod needs only the
		// in-cluster client a running plugin already has. If it can run, it can build.
		Capabilities: []string{"provisioning"},
		Actions: []*pluginv1.ActionDecl{{
			Name:       ActionCreateHost,
			Input:      &pluginv1.ContractRef{SchemaId: "actions/kubecompute/create-host.input"},
			Output:     &pluginv1.ContractRef{SchemaId: "actions/kubecompute/create-host.output"},
			Idempotent: true, // create-or-adopt by name; a re-run of a built host is a no-op
		}},
	}}, nil
}

// Observe projects every host this plugin built, with the address that reaches it.
//
// FULL SYNC each cycle: Kubernetes has no change feed here, and the projection is small because it
// is bounded to what this plugin built. An empty list is a legitimate steady state (nothing built
// yet), so it is emitted as an empty full sync rather than guarded — the mass-tombstone hazard
// kubecontainers guards against comes from a DEGRADED list of a cluster-wide scope, and a scope
// this narrow has nothing to mass-tombstone.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ents, err := s.enumerate(stream.Context())
	if err != nil {
		return err
	}
	// One window carrying the whole (small) set, then the boundary that ends the full sync and
	// lets absent hosts tombstone. FullSync marks the window as part of a full enumeration so the
	// seen-set accrues (ADR-0042); FullSyncComplete is what gates the tombstone.
	if err := stream.Send(&pluginv1.ObserveResponse{Entities: ents, FullSync: true}); err != nil {
		return err
	}
	return stream.Send(&pluginv1.ObserveResponse{FullSync: true, FullSyncComplete: true})
}

func (s *Server) enumerate(ctx context.Context) ([]*pluginv1.ObservedEntity, error) {
	list, err := s.client.CoreV1().Pods(s.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: managedLabel + "=true",
	})
	if err != nil {
		return nil, fmt.Errorf("kubecompute: list built hosts: %w", err)
	}
	out := make([]*pluginv1.ObservedEntity, 0, len(list.Items))
	for i := range list.Items {
		if e := s.project(&list.Items[i]); e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// project maps one built pod onto the host Entity + its reach coordinate.
//
// A pod with NO address yet is projected WITHOUT the Facet rather than skipped: the host exists
// the moment it is built, and the Intent's built-marker label must land even while the address is
// still pending. Omitting the Entity until it was reachable would make the build look unfinished
// and the reconcile would build it again (§1.8 — the wait must be visible, not invisible).
func (s *Server) project(pod *corev1.Pod) *pluginv1.ObservedEntity {
	e := &pluginv1.ObservedEntity{
		Kind:         projectKindOf(pod),
		IdentityKeys: map[string]string{SchemeHost: pod.Namespace + "/" + pod.Name},
		Labels:       map[string]string{},
	}
	// Carry the estate's own labels through — `stratt.intent/instance` among them, which is what
	// the provisioning reconcile matches to decide this instance is built (ADR-0120).
	for k, v := range pod.Labels {
		if k == managedLabel || k == kindLabel {
			continue
		}
		e.Labels[k] = v
	}
	// THE NAME THIS PROVIDER CAUSED, not the pod's IP (ADR-0142 D4). That ADR works the Kubernetes
	// case explicitly: service DNS is deterministic, but "that determinism is the PROVIDER'S
	// knowledge, not the estate's — a K8s Compute provider projects the name it CAUSED, which is
	// the observed producer again". Caused, because the build creates the Service that makes the
	// name resolve; nothing here is derived from a zone the estate guessed at.
	//
	// The pod IP was the first version and is worse in two ways that both bite: it changes on every
	// restart, so a stable Entity acquires an unstable coordinate; and it cannot be a certificate
	// subject — a CA role scoped to a domain refuses an IP, and an IP-CN certificate is wrong even
	// where one would issue it.
	if pod.Status.Phase == corev1.PodRunning {
		raw, err := json.Marshal(map[string]string{
			"address": pod.Name + "." + pod.Namespace + ".svc.cluster.local",
		})
		if err != nil {
			return e
		}
		e.Facets = map[string][]byte{"mgmt.address": raw}
	}
	return e
}

// Invoke routes the build verb.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	if req.GetAction() != ActionCreateHost {
		return status.Errorf(codes.Unimplemented, "kubecompute: unknown action %q", req.GetAction())
	}
	return s.createHost(stream, req)
}

// createHostInput is the build's typed request.
type createHostInput struct {
	// Name is the instance the Intent fanned out to — the host's name and its identity.
	Name string `json:"name"`
	// AuthorizedKeysSecret NAMES a Secret holding authorized_keys; it is a POINTER, never
	// material (§2.5). The build mounts it, so the key that lets Stratt converge the host is the
	// one the estate already brokers for reaching hosts — no key is minted here and none is
	// logged, returned, or written to the graph.
	AuthorizedKeysSecret string `json:"authorizedKeysSecret"`
	// Labels the built host carries, including the Intent's built-marker.
	Labels map[string]string `json:"labels,omitempty"`
	// ProjectKind is the Entity kind the build projects (ADR-0058 M1); "" ⇒ host.
	ProjectKind string `json:"projectKind,omitempty"`
	// Placement and Params are ACCEPTED AND UNUSED on this substrate — a pod is placed by the
	// scheduler, and a host here is defined by its name, image and authorized keys. They are
	// accepted so the shared Intent/Compute launch interface stays shared (ADR-0110), and a
	// non-empty one is REPORTED rather than dropped: an ignored declaration must be visible,
	// which is the whole reason ADR-0123 D3 refuses an input nothing binds.
	Placement map[string]any `json:"placement,omitempty"`
	Params    map[string]any `json:"params,omitempty"`
}

// nonEmpty reports the keys of a declared-but-unused map that actually carried something.
func nonEmpty(m map[string]any) []string {
	var out []string
	for k, v := range m {
		if v == nil {
			continue
		}
		if sv, ok := v.(string); ok && sv == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Server) createHost(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest) error {
	ctx := stream.Context()
	var in createHostInput
	if raw := req.GetArgs().GetBytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return status.Errorf(codes.InvalidArgument, "kubecompute: decode params: %v", err)
		}
	}
	// FAIL CLOSED on the two things a converge-able host cannot be built without. A host with no
	// name has no identity; a host with no authorized keys is built, running, and unreachable —
	// which is exactly the floci failure this plugin exists to not repeat (§1.8).
	if strings.TrimSpace(in.Name) == "" {
		return status.Error(codes.InvalidArgument, "kubecompute: name is required — it is the host's identity")
	}
	if strings.TrimSpace(in.AuthorizedKeysSecret) == "" {
		return status.Error(codes.InvalidArgument,
			"kubecompute: authorizedKeysSecret is required — a host built with no authorized key is "+
				"running and unreachable, which is the failure this provider exists to avoid")
	}

	emit := func(msg string) {
		_ = stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, At: timestamppb.Now(), Message: msg,
		}})
	}
	emit(fmt.Sprintf("building host %s/%s from %s", s.cfg.Namespace, in.Name, s.cfg.Image))
	// SAY WHAT WAS IGNORED. The estate may declare placement and provider params for a fleet whose
	// binding later moves to a substrate that honours them; on this one they do nothing, and an
	// operator reading the Run must be able to see that rather than infer it (§1.8).
	if k := nonEmpty(in.Placement); len(k) > 0 {
		emit("placement " + strings.Join(k, ",") + " ignored: pods are placed by the Kubernetes scheduler, not by the estate")
	}
	if k := nonEmpty(in.Params); len(k) > 0 {
		emit("provider params " + strings.Join(k, ",") + " ignored: a host here is its name, image and authorized keys")
	}

	pod := s.hostPod(in)
	created, err := s.client.CoreV1().Pods(s.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	switch {
	case apierrors.IsAlreadyExists(err):
		// ADOPT rather than fail: the Action declares itself idempotent, and a build re-run for a
		// host that already exists must converge on the same answer instead of erroring at a Gate
		// an operator already approved.
		emit(fmt.Sprintf("host %s already exists — adopting", in.Name))
		created, err = s.client.CoreV1().Pods(s.cfg.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return status.Errorf(codes.Internal, "kubecompute: adopt existing host %s: %v", in.Name, err)
		}
	case err != nil:
		return status.Errorf(codes.Internal, "kubecompute: create host %s: %v", in.Name, err)
	}

	// The Service is what CAUSES the DNS name the Syncer projects. Created after the pod and
	// adopted if present, for the same idempotence reason: a re-run of a built host converges on
	// the same answer rather than erroring at a Gate an operator already approved.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      in.Name,
			Namespace: s.cfg.Namespace,
			Labels:    map[string]string{managedLabel: "true", hostLabel: in.Name},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{hostLabel: in.Name},
			Ports:    []corev1.ServicePort{{Name: "ssh", Port: 22, TargetPort: intstr.FromInt32(22)}},
		},
	}
	if _, serr := s.client.CoreV1().Services(s.cfg.Namespace).Create(ctx, svc, metav1.CreateOptions{}); serr != nil && !apierrors.IsAlreadyExists(serr) {
		// FAIL, rather than leave a host with no resolvable name. A pod without its Service is
		// running and unaddressable — the same shape as a host with no authorized key, and the
		// build must not report success for it (§1.8).
		return status.Errorf(codes.Internal, "kubecompute: create service for host %s: %v", in.Name, serr)
	}
	emit(fmt.Sprintf("host %s reachable at %s.%s.svc.cluster.local", in.Name, in.Name, s.cfg.Namespace))

	outputs, err := json.Marshal(map[string]string{
		"name":     created.Name,
		"identity": created.Namespace + "/" + created.Name,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "kubecompute: marshal outputs: %v", err)
	}
	// The built Entity rides the terminal, WITHOUT mgmt.address: the pod has no address yet and
	// the Syncer projects it when it does. Asserting one here would be stating a fact this code
	// cannot know (§1.2).
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Terminal: true, Ok: true, At: timestamppb.Now(),
			Message: fmt.Sprintf("host %s built", created.Name),
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/kubecompute/create-host.output"},
			Entities:       []*pluginv1.ObservedEntity{s.project(created)},
		},
	})
}

// hostPod renders the host. It is the same shape deploy/dev/managed-web.yaml declares by hand —
// openssh + python3, host keys generated, authorized_keys mounted from a Secret — which is the
// point: what this builds is what the estate already knows how to converge, so nothing downstream
// needs a special case for a built host versus a declared one.
func (s *Server) hostPod(in createHostInput) *corev1.Pod {
	kind := in.ProjectKind
	if kind == "" {
		kind = "host"
	}
	labels := map[string]string{managedLabel: "true", kindLabel: kind, hostLabel: in.Name}
	// The estate's keys VERBATIM. An earlier revision rewrote `stratt.intent/instance` to a
	// domain-prefixed form on the theory that Kubernetes would reject it — it does not:
	// `stratt.intent` is a valid DNS-subdomain label prefix. The rewrite solved nothing and broke
	// the one thing that matters: the reconcile reads ONLY that correlation label to see the
	// instance as built, and the core's governor drops any label key the Actuator did not declare.
	// A renamed marker means the Run goes green and the Finding is offered again forever
	// (the failure core/internal/desiredstate/provisioning_grant_test.go exists to prevent).
	for k, v := range in.Labels {
		labels[k] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      in.Name,
			Namespace: s.cfg.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:    "host",
				Image:   s.cfg.Image,
				Command: []string{"/bin/sh", "-c"},
				Args: []string{`set -e
apk add --no-cache openssh python3 >/dev/null 2>&1
ssh-keygen -A
mkdir -p /root/.ssh
cp /authorized_keys/authorized_keys /root/.ssh/authorized_keys
chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys
sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
exec /usr/sbin/sshd -D -e`},
				Ports: []corev1.ContainerPort{{ContainerPort: 22, Name: "ssh"}},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "authkeys", MountPath: "/authorized_keys", ReadOnly: true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "authkeys",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: in.AuthorizedKeysSecret},
				},
			}},
		},
	}
}

// projectKindOf reads back the kind this host was BUILT to project (ADR-0058 M1), so the Observe
// half and the build agree. Stamped as a label at build time because the pod is the only record
// the Syncer has: deriving it independently would be a second source for one answer.
func projectKindOf(pod *corev1.Pod) string {
	if k := pod.Labels[kindLabel]; k != "" {
		return k
	}
	return "host"
}
