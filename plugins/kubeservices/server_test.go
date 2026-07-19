package kubeservices

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// TestEnumerateAgainstFakeClientset drives the real server path (list Services → map
// corev1.Service → normalize) against a fake clientset — the graphsim/saltsim posture,
// no real cluster. It confirms the corev1→projection mapping and the M:N.
func TestEnumerateAgainstFakeClientset(t *testing.T) {
	helm := map[string]string{
		"app.kubernetes.io/managed-by": "Helm",
		"app.kubernetes.io/instance":   "shop",
		"helm.sh/chart":                "web-stack-1.4.2",
	}
	cs := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web", Labels: helm},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1",
				Selector: map[string]string{"app": "web"},
				Ports:    []corev1.ServicePort{{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(8080)}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "worker", Labels: helm},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9000, Protocol: corev1.ProtocolTCP}}},
		},
		&corev1.Service{ // not Helm-managed
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "legacy"},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolTCP}}},
		},
	)
	s := NewServer(Config{}, cs, slog.New(slog.NewTextHandler(discard{}, nil)))

	ents, err := s.enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(byKind(ents, "service")) != 3 {
		t.Fatalf("want 3 service Entities, got %d", len(byKind(ents, "service")))
	}
	apps := byKind(ents, "application")
	if len(apps) != 1 {
		t.Fatalf("want 1 application (one Helm release), got %d", len(apps))
	}
	provided := 0
	for _, r := range apps[0].GetRelations() {
		if r.GetType() == "provides" {
			provided++
		}
	}
	if provided != 2 {
		t.Fatalf("the release must provide its 2 Services (the M:N), got %d", provided)
	}
}

// TestGetManifest asserts the SYNCER manifest: the two facet Contracts + the
// plugin's own tombstone schemes.
func TestGetManifest(t *testing.T) {
	resp, err := NewServer(Config{}, fake.NewSimpleClientset(), slog.New(slog.NewTextHandler(discard{}, nil))).
		GetManifest(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	got := map[string]bool{}
	for _, c := range m.GetContracts() {
		got[c.GetSchemaId()] = true
	}
	if !got["service.endpoint"] || !got["software.chart"] {
		t.Fatalf("manifest must declare both facet Contracts: %v", got)
	}
	if len(m.GetTombstoneSchemes()) != 2 {
		t.Fatalf("manifest must declare k8s.service + helm.release tombstone schemes: %v", m.GetTombstoneSchemes())
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
