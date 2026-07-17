package secretbroker

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func fakeSecret(ns, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: data}
}

// TestWithMaterial_ResolvesAuthorizedKeys proves the resolver reads ONLY the
// authorized data keys (the ResolvedKey set) from the coordinate-named Secret and
// hands them to the single use closure under their logical names (ADR-0052).
func TestWithMaterial_ResolvesAuthorizedKeys(t *testing.T) {
	cs := fake.NewSimpleClientset(fakeSecret("stratt-secrets", "webhook-sink", map[string][]byte{
		"url": []byte("https://hooks.example/x"), "token": []byte("s3cr3t"), "unrelated": []byte("nope"),
	}))
	r := New(cs, "plugin-ns")
	ref := &pluginv1.ResolvedRef{
		SecretNamespace: "stratt-secrets", SecretName: "webhook-sink",
		Keys: []*pluginv1.ResolvedKey{{Key: "url", Name: "url"}, {Key: "token", Name: "token"}},
	}

	var gotURL, gotToken string
	if err := r.WithMaterial(context.Background(), ref, func(m Material) error {
		gotURL = m.GetString("url")
		gotToken = m.GetString("token")
		if m.Get("unrelated") != nil {
			t.Fatal("only the authorized ResolvedKey set must be resolved, not every Secret key")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithMaterial: %v", err)
	}
	if gotURL != "https://hooks.example/x" || gotToken != "s3cr3t" {
		t.Fatalf("resolved material wrong: url=%q token=%q", gotURL, gotToken)
	}
}

// TestWithMaterial_ZeroizesAfterUse proves MF-B: the material buffer handed to the use
// closure is ZEROIZED once WithMaterial returns — a retained reference sees zeros, so
// material cannot outlive the single use.
func TestWithMaterial_ZeroizesAfterUse(t *testing.T) {
	cs := fake.NewSimpleClientset(fakeSecret("ns", "s", map[string][]byte{"token": []byte("s3cr3t")}))
	r := New(cs, "ns")
	ref := &pluginv1.ResolvedRef{SecretName: "s", Keys: []*pluginv1.ResolvedKey{{Key: "token", Name: "token"}}}

	var leaked []byte
	if err := r.WithMaterial(context.Background(), ref, func(m Material) error {
		leaked = m.Get("token") // retain the live buffer past the closure — a misuse the SDK defends against
		return nil
	}); err != nil {
		t.Fatalf("WithMaterial: %v", err)
	}
	for _, b := range leaked {
		if b != 0 {
			t.Fatalf("MF-B: material must be zeroized after use, found non-zero byte %d", b)
		}
	}
}

// TestWithMaterial_NoCoordinatesFailsClosed proves MF-C: a CredentialRef whose
// coordinates were withheld (a relay, or an unresolved ref) cannot resolve — the
// plugin fails closed rather than reaching for a Secret it was not handed.
func TestWithMaterial_NoCoordinatesFailsClosed(t *testing.T) {
	r := New(fake.NewSimpleClientset(), "ns")
	for _, ref := range []*pluginv1.ResolvedRef{nil, {SecretName: ""}} {
		if err := r.WithMaterial(context.Background(), ref, func(Material) error {
			t.Fatal("use must not run without resolved coordinates")
			return nil
		}); err == nil {
			t.Fatalf("withheld/absent coordinates must fail closed, got nil error for %+v", ref)
		}
	}
}
