package kubeservices

import (
	"encoding/json"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestNormalize_ServiceApplicationProvides proves ADR-0081 slice 1: a Helm-managed
// release with two Services projects two `service` Entities (service.endpoint), one
// `application` Entity (software.chart), and the `provides` M:N edge from the
// application to both services — derived from the Helm labels. A non-Helm Service
// projects a service Entity but no application/provides.
func TestNormalize_ServiceApplicationProvides(t *testing.T) {
	helmLabels := func(instance string) map[string]string {
		return map[string]string{
			"app.kubernetes.io/managed-by": "Helm",
			"app.kubernetes.io/instance":   instance,
			"helm.sh/chart":                "web-stack-1.4.2",
			"app.kubernetes.io/version":    "2.3.0",
		}
	}
	services := []K8sService{
		{Namespace: "prod", Name: "web", Type: "ClusterIP", ClusterIP: "10.0.0.1",
			Ports:    []ServicePort{{Port: 8080, Protocol: "http", Name: "http"}},
			Selector: map[string]string{"app": "web"}, Labels: helmLabels("shop")},
		{Namespace: "prod", Name: "worker", Ports: []ServicePort{{Port: 9000, Protocol: "grpc"}}, Labels: helmLabels("shop")},
		{Namespace: "prod", Name: "legacy", Ports: []ServicePort{{Port: 80}}, Labels: nil}, // not Helm-managed
	}

	ents := Normalize(services, "cluster.local")

	svc := byKind(ents, "service")
	if len(svc) != 3 {
		t.Fatalf("want 3 service Entities, got %d", len(svc))
	}
	apps := byKind(ents, "application")
	if len(apps) != 1 {
		t.Fatalf("want 1 application Entity (one release), got %d", len(apps))
	}
	app := apps[0]
	if app.GetIdentityKeys()[SchemeRelease] != "prod/shop" {
		t.Fatalf("application identity: %v", app.GetIdentityKeys())
	}

	// software.chart is the component-shape (name/version) so it flows through the
	// form-agnostic advisory check — chart CVEs for free.
	var chart struct {
		Charts []struct{ Name, Version, DeliveryForm string }
	}
	if err := json.Unmarshal(app.GetFacets()["software.chart"], &chart); err != nil {
		t.Fatalf("software.chart: %v", err)
	}
	if len(chart.Charts) != 1 || chart.Charts[0].Name != "web-stack" || chart.Charts[0].Version != "1.4.2" || chart.Charts[0].DeliveryForm != "chart" {
		t.Fatalf("software.chart shape wrong: %+v", chart.Charts)
	}

	// app.deliverable is the SCALAR sibling, and it must agree with the list above because both
	// come from the same observation. A Blueprint route's observe expectation can only read this
	// one: facetAtPath walks maps, so `charts.version` resolves to nothing, and `contains` matches
	// a whole element by DeepEqual — which would force the estate to declare appVersion, a fact
	// about the chart rather than desired state (ADR-0148 D3).
	var deliverable struct{ Name, Version string }
	if err := json.Unmarshal(app.GetFacets()["app.deliverable"], &deliverable); err != nil {
		t.Fatalf("app.deliverable: %v", err)
	}
	if deliverable.Name != "web-stack" || deliverable.Version != "1.4.2" {
		t.Fatalf("app.deliverable must agree with software.chart, got %+v", deliverable)
	}

	// provides → BOTH Helm services (the M:N), not the non-Helm one.
	provided := map[string]bool{}
	for _, r := range app.GetRelations() {
		if r.GetType() == "provides" && r.GetToScheme() == SchemeService {
			provided[r.GetToValue()] = true
		}
	}
	if len(provided) != 2 || !provided["prod/web"] || !provided["prod/worker"] {
		t.Fatalf("provides edges wrong: %v", provided)
	}
	if provided["prod/legacy"] {
		t.Fatal("the non-Helm service must not be provided by an application")
	}

	// The service carries its K8s DNS name as dns.fqdn, so a service cert `identifies`
	// it (ADR-0081 slice 3).
	web := byIdentity(t, svc, SchemeService, "prod/web")
	if web.GetIdentityKeys()["dns.fqdn"] != "web.prod.svc.cluster.local" {
		t.Fatalf("service dns.fqdn identity: %v", web.GetIdentityKeys())
	}
	var ep struct {
		Ports []struct {
			Port     int
			Protocol string
		}
		Type string
	}
	if err := json.Unmarshal(web.GetFacets()["service.endpoint"], &ep); err != nil {
		t.Fatalf("service.endpoint: %v", err)
	}
	if len(ep.Ports) != 1 || ep.Ports[0].Port != 8080 || ep.Ports[0].Protocol != "http" || ep.Type != "ClusterIP" {
		t.Fatalf("service.endpoint shape wrong: %+v", ep)
	}
}

func TestParseChart(t *testing.T) {
	cases := []struct{ in, name, version string }{
		{"nginx-15.1.0", "nginx", "15.1.0"},
		{"my-app-1.2.3", "my-app", "1.2.3"},
		{"web-stack-1.4.2", "web-stack", "1.4.2"},
		{"nochart", "nochart", ""}, // no version segment
		{"", "", ""},
	}
	for _, c := range cases {
		n, v := parseChart(c.in)
		if n != c.name || v != c.version {
			t.Errorf("parseChart(%q)=(%q,%q) want (%q,%q)", c.in, n, v, c.name, c.version)
		}
	}
}

func byKind(ents []*pluginv1.ObservedEntity, kind string) []*pluginv1.ObservedEntity {
	var out []*pluginv1.ObservedEntity
	for _, e := range ents {
		if e.GetKind() == kind {
			out = append(out, e)
		}
	}
	return out
}

func byIdentity(t *testing.T, ents []*pluginv1.ObservedEntity, scheme, value string) *pluginv1.ObservedEntity {
	t.Helper()
	for _, e := range ents {
		if e.GetIdentityKeys()[scheme] == value {
			return e
		}
	}
	t.Fatalf("no entity with %s=%s", scheme, value)
	return nil
}

// TestReleaseWithoutInstanceLabelStillProjects: a Helm-managed Service that carries no
// `app.kubernetes.io/instance` must still produce an application Entity.
//
// FOUND LIVE (2026-07-30). The grouping key was `app.kubernetes.io/instance` with a bare
// `continue` when it was absent — and it is Kubernetes' RECOMMENDED label, not a required one.
// podinfo 6.9.2 labels its Service managed-by=Helm, name=podinfo, version=6.9.2,
// helm.sh/chart=podinfo-6.9.2 and no instance, so a plainly-deployed release produced a `service`
// Entity, NO `application` Entity, and no diagnostic. The chart delivery form observed nothing for
// it while every green signal in the repo stayed green.
//
// It survived because the only release the collector had ever been run against was STRATT'S OWN,
// whose chart uses Helm's standard labels helper and therefore does set `instance`. The one case
// that worked satisfied an undeclared assumption — which is the shape of defect this whole arc
// keeps finding, and the reason a fixture that mirrors the working case proves less than it looks.
func TestReleaseWithoutInstanceLabelStillProjects(t *testing.T) {
	// Exactly podinfo 6.9.2's Service labels, transcribed from the live cluster.
	svc := K8sService{
		Namespace: "stratt-apps", Name: "podinfo",
		Ports: []ServicePort{{Port: 9898, Protocol: "http"}},
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "Helm",
			"app.kubernetes.io/name":       "podinfo",
			"app.kubernetes.io/version":    "6.9.2",
			"helm.sh/chart":                "podinfo-6.9.2",
		},
	}
	apps := byKind(Normalize([]K8sService{svc}, "cluster.local"), "application")
	if len(apps) != 1 {
		t.Fatalf("a Helm-managed release with no instance label must still project an application "+
			"Entity — got %d. Silently dropping it makes a deployed release invisible to every "+
			"chart-form Blueprint", len(apps))
	}
	if got := apps[0].GetIdentityKeys()[SchemeRelease]; got != "stratt-apps/podinfo" {
		t.Fatalf("release identity falls back to app.kubernetes.io/name, got %q", got)
	}
	var d struct{ Name, Version string }
	if err := json.Unmarshal(apps[0].GetFacets()["app.deliverable"], &d); err != nil {
		t.Fatalf("app.deliverable: %v", err)
	}
	if d.Name != "podinfo" || d.Version != "6.9.2" {
		t.Fatalf("app.deliverable must carry the chart name+version from helm.sh/chart, got %+v", d)
	}

	// A Service with NEITHER label is still skipped: there is nothing left to name a release by,
	// and inventing an identity is the one guess §1.2 forbids.
	bare := K8sService{Namespace: "x", Name: "y", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"}}
	if apps := byKind(Normalize([]K8sService{bare}, "cluster.local"), "application"); len(apps) != 0 {
		t.Fatalf("a release with no name to derive an identity from must be skipped, got %d", len(apps))
	}
}
