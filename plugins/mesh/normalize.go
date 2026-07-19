// Package mesh is the service-mesh dependency Syncer plugin (ADR-0082 slice 2,
// ADR-0081 `depends-on`): it projects the RUNTIME dependency edges of the service
// dimension. A service→service call observed in mesh request telemetry becomes a
// `service --depends-on--> service` edge; every FQDN that appears (as caller OR
// callee) becomes an identity-anchor `service` Entity so the edge resolves.
//
// It is the SECOND source of `depends-on` (a declared source is the other), which is
// exactly why it needed relation liveness (ADR-0082): a dependency co-asserted by the
// mesh AND a declaration stays live until BOTH stop, collected on the last retraction.
//
// Nothing is baked in (charter §1.4/§1.5): the mesh flavor — Istio, Linkerd, Consul,
// Cilium — is not code, it is the PromQL query + label names in the transport config.
// This normalizer is pure content-expertise over an abstract TrafficEdge, so it is
// fixture-testable without any mesh, metrics backend, or cluster. The plugin holds no
// graph write path (§1.2): it proposes typed values; the core-side host governs writes
// (ownership, identity gating, Run provenance) and the per-source relation-presence GC.
package mesh

import (
	"sort"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TrafficEdge is one observed caller→callee service dependency the collector reads
// from mesh request telemetry (the transport maps a metrics result vector onto these).
// Identity is the callee/caller service DNS name — the SHARED `dns.fqdn` scheme, so
// the edge lands on the SAME `service` Entity a K8s Service (ADR-0081 kubeservices)
// and a service mTLS cert (ADR-0081 slice 3) already correlate on. Kept free of any
// metrics client so the content-expertise is fixture-testable.
type TrafficEdge struct {
	FromFQDN string // the calling service's DNS name
	ToFQDN   string // the called service's DNS name
}

// SchemeFQDN is the shared service-identity scheme the mesh anchors on and targets.
const SchemeFQDN = "dns.fqdn"

// Normalize maps a FULL snapshot of observed traffic edges onto ObservedEntities: one
// identity-anchor `service` Entity per distinct FQDN that appears (caller or callee —
// so every `depends-on` target resolves; the host DROPS edges to unknown targets, it
// never vivifies), each caller carrying a `depends-on` ObservedRelation to the services
// it calls. The anchor carries NO Facet and NO label: the mesh co-asserts a service's
// EXISTENCE and its dependency edges, nothing more — kubeservices owns service.endpoint,
// the mesh owns the runtime edges (§2.1 single write-owner per namespace). Self-edges (a
// service calling itself) are dropped — not a dependency. Duplicate edges collapse.
// Deterministic order for stable projection and tests.
func Normalize(edges []TrafficEdge) []*pluginv1.ObservedEntity {
	// Distinct FQDNs (anchors) and distinct caller→callee edges, both order-independent.
	anchors := map[string]struct{}{}
	deps := map[string]map[string]struct{}{} // fromFQDN -> set of toFQDN

	for _, e := range edges {
		if e.FromFQDN == "" || e.ToFQDN == "" || e.FromFQDN == e.ToFQDN {
			continue // incomplete or self-edge — not a dependency
		}
		anchors[e.FromFQDN] = struct{}{}
		anchors[e.ToFQDN] = struct{}{}
		if deps[e.FromFQDN] == nil {
			deps[e.FromFQDN] = map[string]struct{}{}
		}
		deps[e.FromFQDN][e.ToFQDN] = struct{}{}
	}

	fqdns := make([]string, 0, len(anchors))
	for f := range anchors {
		fqdns = append(fqdns, f)
	}
	sort.Strings(fqdns)

	out := make([]*pluginv1.ObservedEntity, 0, len(fqdns))
	for _, fqdn := range fqdns {
		out = append(out, serviceAnchor(fqdn, deps[fqdn]))
	}
	return out
}

// serviceAnchor builds the identity-only `service` Entity for one FQDN, carrying its
// outbound `depends-on` edges (targets ordered for determinism).
func serviceAnchor(fqdn string, callees map[string]struct{}) *pluginv1.ObservedEntity {
	rels := make([]*pluginv1.ObservedRelation, 0, len(callees))
	if len(callees) > 0 {
		targets := make([]string, 0, len(callees))
		for t := range callees {
			targets = append(targets, t)
		}
		sort.Strings(targets)
		for _, t := range targets {
			rels = append(rels, &pluginv1.ObservedRelation{Type: "depends-on", ToScheme: SchemeFQDN, ToValue: t})
		}
	}
	return &pluginv1.ObservedEntity{
		Kind:         "service",
		IdentityKeys: map[string]string{SchemeFQDN: fqdn},
		Relations:    rels,
	}
}
