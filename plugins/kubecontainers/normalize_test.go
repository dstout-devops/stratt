package kubecontainers

import (
	"encoding/json"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

type containerFacet struct {
	Containers []struct {
		Name, Version, Digest, Origin, DeliveryForm string
	} `json:"containers"`
}

func decodeContainers(t *testing.T, e *pluginv1.ObservedEntity) containerFacet {
	t.Helper()
	raw, ok := e.GetFacets()["software.container"]
	if !ok {
		t.Fatalf("node %v carries no software.container facet", e.GetIdentityKeys())
	}
	var f containerFacet
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode software.container: %v", err)
	}
	return f
}

func byNode(ents []*pluginv1.ObservedEntity) map[string]*pluginv1.ObservedEntity {
	m := map[string]*pluginv1.ObservedEntity{}
	for _, e := range ents {
		m[e.GetIdentityKeys()[SchemeNode]] = e
	}
	return m
}

const testCluster = "cluster-x"

// node is the cluster-qualified identity value the collector emits for a node.
func node(name string) string { return testCluster + "/" + name }

// TestNormalize_PerNodeInventory: images aggregate onto the node (host) that runs them,
// deduped across pods, as software.container components (deliveryForm "container") — the
// shape the form-agnostic advisory check consumes.
func TestNormalize_PerNodeInventory(t *testing.T) {
	pods := []Pod{
		{Namespace: "prod", Name: "web-1", NodeName: "node-a", Containers: []Container{
			{Image: "nginx:1.25.3", ImageID: "docker-pullable://nginx@sha256:aaa"},
		}},
		{Namespace: "prod", Name: "web-2", NodeName: "node-a", Containers: []Container{
			{Image: "nginx:1.25.3", ImageID: "docker-pullable://nginx@sha256:aaa"}, // dup across pods
		}},
		{Namespace: "prod", Name: "api-1", NodeName: "node-b", Containers: []Container{
			{Image: "ghcr.io/acme/api:v2", ImageID: "ghcr.io/acme/api@sha256:bbb"},
		}},
	}
	out := Normalize(pods, Config{ClusterID: testCluster})
	if len(out) != 2 {
		t.Fatalf("expected 2 node hosts, got %d", len(out))
	}
	m := byNode(out)

	for _, e := range out {
		if e.GetKind() != "host" {
			t.Fatalf("anchor kind must be `host`, got %q", e.GetKind())
		}
		if len(e.GetLabels()) != 0 {
			t.Fatalf("no labels (label clobber, §2.4), got %v", e.GetLabels())
		}
	}

	a := decodeContainers(t, m[node("node-a")])
	if len(a.Containers) != 1 {
		t.Fatalf("node-a: nginx must dedup across web-1/web-2, got %d", len(a.Containers))
	}
	c := a.Containers[0]
	if c.Name != "nginx" || c.Version != "1.25.3" || c.Digest != "sha256:aaa" || c.DeliveryForm != "container" {
		t.Fatalf("node-a component mismapped: %+v", c)
	}
	if c.Origin != "" {
		t.Fatalf("origin must not be guessed, got %q", c.Origin)
	}
	b := decodeContainers(t, m[node("node-b")])
	if b.Containers[0].Name != "ghcr.io/acme/api" || b.Containers[0].Version != "v2" {
		t.Fatalf("node-b component mismapped: %+v", b.Containers[0])
	}
}

// TestNormalize_ClusterQualifiedIdentity is the guardian §1.2 must-fix: two clusters'
// identically-named nodes must NOT collapse — the node identity is qualified by cluster,
// so `worker-1` in cluster-a and cluster-b are distinct `host` entities.
func TestNormalize_ClusterQualifiedIdentity(t *testing.T) {
	pods := []Pod{{NodeName: "worker-1", Containers: []Container{{Image: "nginx:1"}}}}
	a := Normalize(pods, Config{ClusterID: "cluster-a"})
	b := Normalize(pods, Config{ClusterID: "cluster-b"})

	idA := a[0].GetIdentityKeys()[SchemeNode]
	idB := b[0].GetIdentityKeys()[SchemeNode]
	if idA == idB {
		t.Fatalf("identically-named nodes in different clusters must not share identity: both %q", idA)
	}
	if idA != "cluster-a/worker-1" || idB != "cluster-b/worker-1" {
		t.Fatalf("node identity must be cluster-qualified, got %q / %q", idA, idB)
	}
}

// TestNormalize_SkipsUnscheduled: a pod not yet scheduled to a node contributes no
// inventory (it is not running on any host).
func TestNormalize_SkipsUnscheduled(t *testing.T) {
	out := Normalize([]Pod{
		{Namespace: "prod", Name: "pending", NodeName: "", Containers: []Container{{Image: "busybox:1"}}},
	}, Config{ClusterID: testCluster})
	if len(out) != 0 {
		t.Fatalf("unscheduled pod must project nothing, got %d entities", len(out))
	}
}

// TestNormalize_InternalRegistryOrigin: only images from the configured internal
// registry are tagged origin "internal"; others get no origin (lineage never guessed).
func TestNormalize_InternalRegistryOrigin(t *testing.T) {
	out := Normalize([]Pod{
		{NodeName: "n", Containers: []Container{
			{Image: "registry.internal.acme/payments:1.4"},
			{Image: "nginx:1.25"},
		}},
	}, Config{ClusterID: testCluster, InternalRegistry: "registry.internal.acme/"})
	f := decodeContainers(t, out[0])
	got := map[string]string{}
	for _, c := range f.Containers {
		got[c.Name] = c.Origin
	}
	if got["registry.internal.acme/payments"] != "internal" {
		t.Fatalf("internal image must be origin=internal, got %q", got["registry.internal.acme/payments"])
	}
	if got["nginx"] != "" {
		t.Fatalf("external image must have no origin, got %q", got["nginx"])
	}
}

func TestParseImageRef(t *testing.T) {
	cases := []struct{ ref, name, version string }{
		{"nginx:1.25.3", "nginx", "1.25.3"},
		{"nginx", "nginx", "latest"},                                    // no tag ⇒ Docker default
		{"ghcr.io/acme/api:v2", "ghcr.io/acme/api", "v2"},               // registry path
		{"registry:5000/team/app:1.0", "registry:5000/team/app", "1.0"}, // host:port not mistaken for tag
		{"redis@sha256:deadbeef", "redis", "latest"},                    // inline digest stripped, no tag
		{"nginx:1.25@sha256:deadbeef", "nginx", "1.25"},                 // tag + inline digest
	}
	for _, c := range cases {
		name, version := parseImageRef(c.ref)
		if name != c.name || version != c.version {
			t.Errorf("parseImageRef(%q) = (%q,%q), want (%q,%q)", c.ref, name, version, c.name, c.version)
		}
	}
}

func TestParseDigest(t *testing.T) {
	cases := []struct{ imageID, want string }{
		{"docker-pullable://nginx@sha256:abc", "sha256:abc"},
		{"nginx@sha256:abc", "sha256:abc"},
		{"sha256:abc", "sha256:abc"},
		{"", ""},
		{"nginx:1.25", ""}, // no digest present
	}
	for _, c := range cases {
		if got := parseDigest(c.imageID); got != c.want {
			t.Errorf("parseDigest(%q) = %q, want %q", c.imageID, got, c.want)
		}
	}
}

// TestNormalize_InlineDigestOnSpec: when the runtime reports no imageID but the spec ref
// pins a digest inline, the digest is still captured.
func TestNormalize_InlineDigestOnSpec(t *testing.T) {
	out := Normalize([]Pod{
		{NodeName: "n", Containers: []Container{{Image: "nginx:1.25@sha256:pinned", ImageID: ""}}},
	}, Config{ClusterID: testCluster})
	c := decodeContainers(t, out[0]).Containers[0]
	if c.Digest != "sha256:pinned" {
		t.Fatalf("inline spec digest must be captured, got %q", c.Digest)
	}
}
