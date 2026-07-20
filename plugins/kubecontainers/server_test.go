package kubecontainers

import (
	"context"
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

type captureStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pluginv1.ObserveResponse
}

func (c *captureStream) Context() context.Context { return c.ctx }
func (c *captureStream) Send(r *pluginv1.ObserveResponse) error {
	c.sent = append(c.sent, r)
	return nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestManifest_ClaimsContainerOwnerTombstoneNode: the collector claims the
// software.container namespace as its single §2.1 write-owner and tombstones on the node
// scheme.
func TestManifest_ClaimsContainerOwnerTombstoneNode(t *testing.T) {
	s := NewServer(ServerConfig{}, fake.NewSimpleClientset(), discard())
	resp, err := s.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Fatalf("class must be SYNCER, got %v", m.GetClass())
	}
	var claimsContainer bool
	for _, c := range m.GetContracts() {
		if c.GetSchemaId() == "software.container" {
			claimsContainer = true
		}
	}
	if !claimsContainer {
		t.Fatal("must claim software.container as the §2.1 write-owner")
	}
	if got := m.GetTombstoneSchemes(); len(got) != 1 || got[0] != SchemeNode {
		t.Fatalf("tombstone scheme must be k8s.node, got %v", got)
	}
	if m.GetPluginId() != "kubecontainers" {
		t.Fatalf("default plugin id must be `kubecontainers`, got %q", m.GetPluginId())
	}
}

// kubeSystem is the namespace the collector reads to auto-resolve the cluster qualifier.
func kubeSystem(uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}
}

// TestObserve_ProjectsNodesFromPods: a live full sync reads Pods via client-go, resolves
// the cluster qualifier from the kube-system UID, and emits one host per node (identity
// cluster-qualified) with its container inventory, ending on full_sync_complete.
func TestObserve_ProjectsNodesFromPods(t *testing.T) {
	client := fake.NewSimpleClientset(
		kubeSystem("clus-123"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web-1"},
			Spec: corev1.PodSpec{
				NodeName:   "node-a",
				Containers: []corev1.Container{{Name: "web", Image: "nginx:1.25.3"}},
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{Name: "web", ImageID: "docker-pullable://nginx@sha256:aaa"}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api-1"},
			Spec: corev1.PodSpec{
				NodeName:   "node-b",
				Containers: []corev1.Container{{Name: "api", Image: "ghcr.io/acme/api:v2"}},
			},
		},
	)
	s := NewServer(ServerConfig{}, client, discard())
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || !stream.sent[0].GetFullSyncComplete() {
		t.Fatalf("expected one full-sync-complete response, got %+v", stream.sent)
	}
	ents := stream.sent[0].GetEntities()
	if len(ents) != 2 {
		t.Fatalf("expected node-a + node-b hosts, got %d", len(ents))
	}
	m := byNode(ents)
	host, ok := m["clus-123/node-a"]
	if !ok {
		t.Fatalf("node-a must be projected with the cluster-qualified identity, got %v", func() []string {
			var ks []string
			for k := range m {
				ks = append(ks, k)
			}
			return ks
		}())
	}
	f := decodeContainers(t, host)
	if f.Containers[0].Digest != "sha256:aaa" {
		t.Fatalf("node-a digest from status.imageID expected, got %q", f.Containers[0].Digest)
	}
}

// TestObserve_EmptySnapshotHoldsSteady is the guardian §1.8 must-fix: an empty pod list
// (most often a transient/RBAC/scope issue) must NOT emit a full-sync boundary — that
// would tombstone every node's container inventory and silence the advisory check.
func TestObserve_EmptySnapshotHoldsSteady(t *testing.T) {
	client := fake.NewSimpleClientset(kubeSystem("clus-123")) // kube-system present, but zero pods
	s := NewServer(ServerConfig{}, client, discard())
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetFullSyncComplete() {
		t.Fatalf("an empty snapshot must NOT assert full_sync_complete, got %+v", stream.sent)
	}
}

// TestObserve_EmptySnapshotAllowed: an operator running a cluster legitimately expected
// to be empty opts in, and the empty full sync IS asserted (a real drain collects).
func TestObserve_EmptySnapshotAllowed(t *testing.T) {
	client := fake.NewSimpleClientset(kubeSystem("clus-123"))
	s := NewServer(ServerConfig{AllowEmptyFullSync: true}, client, discard())
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if !stream.sent[0].GetFullSyncComplete() {
		t.Fatal("with AllowEmptyFullSync, an empty snapshot must assert the full sync")
	}
}

// TestObserve_ConfiguredClusterIDWins: an operator-set ClusterID is used verbatim and no
// kube-system lookup is needed (the fake has none, proving no lookup happens).
func TestObserve_ConfiguredClusterIDWins(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web-1"},
		Spec:       corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "w", Image: "nginx:1"}}},
	})
	s := NewServer(ServerConfig{Config: Config{ClusterID: "prod-us-east"}}, client, discard())
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if _, ok := byNode(stream.sent[0].GetEntities())["prod-us-east/n1"]; !ok {
		t.Fatal("a configured ClusterID must qualify the node identity verbatim")
	}
}

// TestObserve_UnresolvableClusterIDFailsLoud: no configured id AND no kube-system UID
// must fail the sync (§1.8) rather than emit ambiguous unqualified node identities.
func TestObserve_UnresolvableClusterIDFailsLoud(t *testing.T) {
	client := fake.NewSimpleClientset() // no kube-system namespace
	s := NewServer(ServerConfig{}, client, discard())
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err == nil {
		t.Fatal("an unresolvable cluster id must fail loudly, not emit unqualified identities")
	}
}
