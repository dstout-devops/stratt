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
