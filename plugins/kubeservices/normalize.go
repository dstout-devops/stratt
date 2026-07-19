// Package kubeservices is the K8s Services Syncer plugin (ADR-0081): it projects the
// service/capability dimension. A K8s Service becomes a `service` Entity carrying a
// service.endpoint Facet; a Helm-managed release becomes a form-neutral `application`
// Entity carrying a software.chart Facet (the deliverable), and the `provides` M:N
// edge (application → service) is DERIVED from the K8s app.kubernetes.io Helm labels
// — never guessed. One scrape feeds both dimensions and the seam between them.
//
// Pure content-expertise: it maps the projection-relevant shape of a Service to wire
// ObservedEntity/ObservedRelation values; the live client-go transport (the server)
// maps corev1.Service onto that shape. The plugin holds no graph write path (§1.2).
package kubeservices

import (
	"encoding/json"
	"sort"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// K8sService is the projection-relevant shape of a Kubernetes Service the collector
// reads (the server maps corev1.Service onto this). Kept free of client-go so the
// content-expertise is fixture-testable without a cluster.
type K8sService struct {
	Namespace string
	Name      string
	Type      string // ClusterIP | NodePort | LoadBalancer | ExternalName
	ClusterIP string
	Ports     []ServicePort
	Selector  map[string]string
	Labels    map[string]string // includes the app.kubernetes.io/* + helm.sh/chart labels
}

// ServicePort is one exposed port.
type ServicePort struct {
	Name       string
	Port       int
	TargetPort string
	Protocol   string // OPEN: http|grpc|tcp|udp|… (K8s reports L4; L7 refined by annotations later)
}

// Identity schemes this plugin emits + targets (the grant/tombstone surface).
const (
	SchemeService = "k8s.service"  // "<namespace>/<name>"
	SchemeRelease = "helm.release" // "<namespace>/<instance>"
)

// Normalize maps a full enumeration of K8s Services onto ObservedEntities: one
// `service` Entity per Service, and one `application` Entity per Helm release that
// `provides` the services it owns. Deterministic order for stable projection/tests.
func Normalize(services []K8sService) []*pluginv1.ObservedEntity {
	sort.Slice(services, func(i, j int) bool {
		if services[i].Namespace != services[j].Namespace {
			return services[i].Namespace < services[j].Namespace
		}
		return services[i].Name < services[j].Name
	})

	out := make([]*pluginv1.ObservedEntity, 0, len(services))
	// Group Helm-managed services by release to build application Entities that
	// provide many services (the M:N).
	type release struct {
		namespace, instance, chartLabel, appVersion string
		serviceKeys                                 []string
	}
	releases := map[string]*release{}
	var releaseOrder []string

	for _, s := range services {
		key := s.Namespace + "/" + s.Name
		out = append(out, serviceEntity(s, key))

		if !isHelmManaged(s.Labels) {
			continue
		}
		instance := s.Labels["app.kubernetes.io/instance"]
		if instance == "" {
			continue
		}
		rk := s.Namespace + "/" + instance
		r, ok := releases[rk]
		if !ok {
			r = &release{namespace: s.Namespace, instance: instance,
				chartLabel: s.Labels["helm.sh/chart"], appVersion: s.Labels["app.kubernetes.io/version"]}
			releases[rk] = r
			releaseOrder = append(releaseOrder, rk)
		}
		r.serviceKeys = append(r.serviceKeys, key)
	}

	for _, rk := range releaseOrder {
		out = append(out, applicationEntity(releases[rk].namespace, releases[rk].instance,
			releases[rk].chartLabel, releases[rk].appVersion, releases[rk].serviceKeys))
	}
	return out
}

func serviceEntity(s K8sService, key string) *pluginv1.ObservedEntity {
	ports := make([]map[string]any, 0, len(s.Ports))
	for _, p := range s.Ports {
		port := map[string]any{"port": p.Port}
		if p.Name != "" {
			port["name"] = p.Name
		}
		if p.TargetPort != "" {
			port["targetPort"] = p.TargetPort
		}
		if p.Protocol != "" {
			port["protocol"] = p.Protocol
		}
		ports = append(ports, port)
	}
	endpoint := map[string]any{"ports": ports}
	if s.ClusterIP != "" {
		endpoint["clusterAddress"] = s.ClusterIP
	}
	if s.Type != "" {
		endpoint["type"] = s.Type
	}
	if len(s.Selector) > 0 {
		endpoint["selector"] = s.Selector
	}
	raw, _ := json.Marshal(endpoint)
	return &pluginv1.ObservedEntity{
		Kind:         "service",
		IdentityKeys: map[string]string{SchemeService: key},
		Labels:       map[string]string{"service.name": s.Name},
		Facets:       map[string][]byte{"service.endpoint": raw},
	}
}

func applicationEntity(namespace, instance, chartLabel, appVersion string, serviceKeys []string) *pluginv1.ObservedEntity {
	name, version := parseChart(chartLabel)
	chart := map[string]any{"deliveryForm": "chart"}
	if name != "" {
		chart["name"] = name
	} else {
		chart["name"] = instance // fall back to the release name when the chart label is absent
	}
	if version != "" {
		chart["version"] = version
	}
	if appVersion != "" {
		chart["appVersion"] = appVersion
	}
	// software.chart is a `charts` component list (the ADR-0080 software-component
	// shape), so a vulnerable chart version flows through CheckSoftwareAdvisories's
	// form-agnostic `software.%` pass for free.
	raw, _ := json.Marshal(map[string]any{"charts": []map[string]any{chart}})

	rels := make([]*pluginv1.ObservedRelation, 0, len(serviceKeys))
	for _, key := range serviceKeys {
		rels = append(rels, &pluginv1.ObservedRelation{Type: "provides", ToScheme: SchemeService, ToValue: key})
	}
	return &pluginv1.ObservedEntity{
		Kind:         "application",
		IdentityKeys: map[string]string{SchemeRelease: namespace + "/" + instance},
		Labels:       map[string]string{"application.name": instance},
		Facets:       map[string][]byte{"software.chart": raw},
		Relations:    rels,
	}
}

func isHelmManaged(labels map[string]string) bool {
	return labels["app.kubernetes.io/managed-by"] == "Helm"
}

// parseChart splits a helm.sh/chart label ("<name>-<version>", e.g. "nginx-15.1.0"
// or "my-app-1.2.3") into name + version: the version is the segment after the LAST
// dash that begins with a digit (chart names may themselves contain dashes). Returns
// empty strings when the label is absent or unparseable (a loud fallback, not a lie).
func parseChart(label string) (name, version string) {
	if label == "" {
		return "", ""
	}
	i := strings.LastIndex(label, "-")
	if i <= 0 || i == len(label)-1 {
		return label, ""
	}
	suffix := label[i+1:]
	if suffix[0] >= '0' && suffix[0] <= '9' {
		return label[:i], suffix
	}
	return label, ""
}
