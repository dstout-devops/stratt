// Package kubecontainers is the K8s container-image Syncer plugin (ADR-0080): it is
// the CONTAINER-form write-owner of the deliverable-software dimension — the sibling of
// the salt OS-package collector. It reads running Pods, aggregates the container images
// per node, and projects a `software.container` Facet onto a `host` Entity keyed by the
// node (`k8s.node`) — the direct analog of `software.package` on a host. A vulnerable
// base image then flows through the ONE form-agnostic CheckSoftwareAdvisories pass with
// no check change (the slice-3 software-component convention), turning running images
// into patch/vulnerability-remediation signal.
//
// Node grain (not pod): a node is a stable identity, so pod-name churn under rollouts
// does not thrash the graph, and every image on the node is covered regardless of
// workload type. Per-workload remediation precision is a documented §1.1 follow-up, not
// built speculatively here.
//
// Pure content-expertise: it maps the projection-relevant shape of a Pod onto wire
// ObservedEntity values; the live client-go transport (the server) maps corev1.Pod onto
// that shape. The plugin holds no graph write path (§1.2).
package kubecontainers

import (
	"encoding/json"
	"sort"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Pod is the projection-relevant shape of a running Kubernetes Pod (the server maps
// corev1.Pod onto this). Kept free of client-go so the content-expertise is fixture-
// testable without a cluster.
type Pod struct {
	Namespace  string
	Name       string
	NodeName   string // the node the pod is scheduled on ("" ⇒ unscheduled, skipped)
	Containers []Container
}

// Container is one container in a pod: the spec image reference and the resolved status
// imageID (which carries the pulled digest).
type Container struct {
	Image   string // spec reference, e.g. "nginx:1.25.3", "ghcr.io/acme/api:v1", "redis"
	ImageID string // status imageID, e.g. "docker-pullable://nginx@sha256:…" or "nginx@sha256:…"
}

// SchemeNode is the node-identity scheme the collector anchors the inventory on. Its
// value is cluster-qualified — "<clusterID>/<nodeName>" — so it is globally unique
// across the multi-cluster estate (§1.2), the analog of salt's estate-unique minion id.
const SchemeNode = "k8s.node"

// Config tunes the projection.
type Config struct {
	// ClusterID qualifies the node identity so it is GLOBALLY unique (§1.2). A K8s node
	// name (`worker-1`, `master`) is unique only WITHIN a cluster; with one collector per
	// cluster (the multi-cluster "one logical estate" posture, ADR-0044), two clusters'
	// identically-named nodes would otherwise collapse onto one `host` Entity carrying the
	// union of two unrelated boxes' images — a projection that misrepresents reality. The
	// server supplies this (an operator-set id, else the authoritative kube-system
	// namespace UID); Normalize requires it non-empty and refuses to emit an ambiguous
	// node identity without it.
	ClusterID string

	// InternalRegistry, when set, marks an image whose repository begins with this
	// prefix as origin "internal" (a first-party build). Images that do not match get
	// NO origin — lineage is never guessed (§1.8 honest-over-plausible). Empty ⇒ no
	// image is tagged internal.
	InternalRegistry string
}

// Normalize maps a full enumeration of running Pods onto ObservedEntities: one `host`
// Entity per node, carrying a `software.container` Facet whose component list is the
// DEDUPED union of the container images running on that node. Unscheduled pods (no
// node) are skipped. Deterministic order for stable projection/tests.
func Normalize(pods []Pod, cfg Config) []*pluginv1.ObservedEntity {
	// node -> set of distinct components (keyed by name\x00version\x00digest).
	byNode := map[string]map[string]component{}
	for _, p := range pods {
		if p.NodeName == "" {
			continue // unscheduled — not running on any host yet
		}
		set := byNode[p.NodeName]
		if set == nil {
			set = map[string]component{}
			byNode[p.NodeName] = set
		}
		for _, c := range p.Containers {
			comp := toComponent(c, cfg)
			if comp.name == "" {
				continue // unparseable reference — skip, never emit a nameless component
			}
			set[comp.name+"\x00"+comp.version+"\x00"+comp.digest] = comp
		}
	}

	nodes := make([]string, 0, len(byNode))
	for n := range byNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	out := make([]*pluginv1.ObservedEntity, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, nodeEntity(cfg.ClusterID, node, byNode[node]))
	}
	return out
}

type component struct {
	name, version, digest, origin string
}

func nodeEntity(clusterID, node string, comps map[string]component) *pluginv1.ObservedEntity {
	list := make([]component, 0, len(comps))
	for _, c := range comps {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].name != list[j].name {
			return list[i].name < list[j].name
		}
		if list[i].version != list[j].version {
			return list[i].version < list[j].version
		}
		return list[i].digest < list[j].digest
	})

	items := make([]map[string]any, 0, len(list))
	for _, c := range list {
		item := map[string]any{
			"name":         c.name,
			"version":      c.version,
			"deliveryForm": "container",
		}
		if c.digest != "" {
			item["digest"] = c.digest
		}
		if c.origin != "" {
			item["origin"] = c.origin
		}
		items = append(items, item)
	}
	raw, _ := json.Marshal(map[string]any{"containers": items})

	return &pluginv1.ObservedEntity{
		Kind: "host",
		// Cluster-qualified so the node identity is globally unique (§1.2, must-fix): a
		// node name is unique only within its cluster.
		IdentityKeys: map[string]string{SchemeNode: clusterID + "/" + node},
		// NO labels: the label bag is a whole-set last-writer projection that clobbers
		// across Sources correlating onto one host (§2.4). The container inventory lives
		// in its own source-scoped facet.
		Facets: map[string][]byte{"software.container": raw},
	}
}

// toComponent maps a container onto a software component: name = image repository,
// version = tag, digest from the imageID (or an inline @sha256 on the spec ref).
func toComponent(c Container, cfg Config) component {
	name, version := parseImageRef(c.Image)
	digest := parseDigest(c.ImageID)
	if digest == "" {
		// A digest may be pinned inline on the spec reference itself.
		if _, d := splitDigest(c.Image); d != "" {
			digest = d
		}
	}
	comp := component{name: name, version: version, digest: digest}
	if cfg.InternalRegistry != "" && strings.HasPrefix(name, cfg.InternalRegistry) {
		comp.origin = "internal"
	}
	return comp
}

// parseImageRef splits an image reference into repository (name) and tag (version). It
// strips any inline digest first, then takes the tag as the segment after the LAST ":"
// that follows the last "/" (so a registry host:port is not mistaken for a tag). A
// reference with no tag defaults to "latest" (Docker's own default), never guessed away.
func parseImageRef(ref string) (name, version string) {
	ref, _ = splitDigest(ref)
	if ref == "" {
		return "", ""
	}
	lastSlash := strings.LastIndex(ref, "/")
	tagColon := strings.LastIndex(ref, ":")
	if tagColon > lastSlash { // a colon in the final path segment is the tag separator
		return ref[:tagColon], ref[tagColon+1:]
	}
	return ref, "latest"
}

// splitDigest separates a reference from a trailing "@sha256:…" digest.
func splitDigest(ref string) (repo, digest string) {
	if repo, digest, ok := strings.Cut(ref, "@"); ok {
		return repo, digest
	}
	return ref, ""
}

// parseDigest extracts the "sha256:…" digest from a status imageID, which may be
// prefixed ("docker-pullable://…") and/or carry the repository ("repo@sha256:…").
func parseDigest(imageID string) string {
	if imageID == "" {
		return ""
	}
	if _, digest, ok := strings.Cut(imageID, "@"); ok {
		return digest
	}
	// Some runtimes report a bare "sha256:…" with no repository prefix.
	if i := strings.Index(imageID, "sha256:"); i >= 0 {
		return imageID[i:]
	}
	return ""
}
