package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeStream captures the InvokeResponses the plugin streams.
type fakeStream struct {
	grpc.ServerStreamingServer[pluginv1.InvokeResponse]
	ctx  context.Context
	sent []*pluginv1.InvokeResponse
}

func (f *fakeStream) Send(r *pluginv1.InvokeResponse) error { f.sent = append(f.sent, r); return nil }
func (f *fakeStream) Context() context.Context              { return f.ctx }

func (f *fakeStream) terminal() *pluginv1.TaskEvent {
	for _, r := range f.sent {
		if ev := r.GetEvent(); ev.GetTerminal() {
			return ev
		}
	}
	return nil
}

func newServer(t *testing.T, secretURL string) *Server {
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "stratt-secrets", Name: "webhook-sink"},
		Data:       map[string][]byte{"url": []byte(secretURL), "token": []byte("s3cr3t")},
	})
	return New("notify", secretbroker.New(cs, "stratt-secrets"), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func webhookRef() *pluginv1.CredentialRef {
	return &pluginv1.CredentialRef{
		Name: "cred/webhook",
		Resolved: &pluginv1.ResolvedRef{
			SecretNamespace: "stratt-secrets", SecretName: "webhook-sink",
			Keys: []*pluginv1.ResolvedKey{{Key: "url", Name: "url"}, {Key: "token", Name: "token"}},
		},
	}
}

func invoke(s *Server, args any, creds ...*pluginv1.CredentialRef) *fakeStream {
	raw, _ := json.Marshal(args)
	st := &fakeStream{ctx: context.Background()}
	_ = s.Invoke(&pluginv1.InvokeRequest{
		Envelope: &pluginv1.Envelope{Creds: creds},
		Args:     &pluginv1.Payload{Bytes: raw},
		Action:   actionWebhook,
	}, st)
	return st
}

// TestInvoke_DeliversWithResolvedCredential proves the plugin resolves the Sink's
// url/token via the SecretBroker (from the core-provided coordinates) and POSTs the
// body with the bearer token, ending with a terminal ok (ADR-0052/0046).
func TestInvoke_DeliversWithResolvedCredential(t *testing.T) {
	var gotBody, gotAuth, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotAuth, gotCT = string(b), r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	st := invoke(newServer(t, srv.URL), map[string]any{"body": `{"msg":"hi"}`, "credentialMount": "cred/webhook"}, webhookRef())
	term := st.terminal()
	if term == nil || !term.GetOk() {
		t.Fatalf("delivery must end with a terminal ok, got %+v", term)
	}
	if gotBody != `{"msg":"hi"}` || gotAuth != "Bearer s3cr3t" || gotCT != "application/json" {
		t.Fatalf("request wrong: body=%q auth=%q ct=%q", gotBody, gotAuth, gotCT)
	}
}

// TestInvoke_Non2xxFailsTerminally proves a non-2xx response folds to a terminal
// not-ok carrying only a sanitized status class — never the url/token/body (§2.5).
func TestInvoke_Non2xxFailsTerminally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := invoke(newServer(t, srv.URL), map[string]any{"body": "x", "credentialMount": "cred/webhook"}, webhookRef())
	term := st.terminal()
	if term == nil || term.GetOk() {
		t.Fatalf("a 500 must fold to terminal not-ok, got %+v", term)
	}
	if term.GetFields()["detail"] != "http 500" {
		t.Fatalf("verdict must be a sanitized status class, got %q", term.GetFields()["detail"])
	}
}

// TestInvoke_WithheldCoordinatesFailClosed proves MF-C: a CredentialRef with no
// resolved coordinates (a relay withheld them) cannot resolve — terminal not-ok, no
// POST attempted.
func TestInvoke_WithheldCoordinatesFailClosed(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posted = true }))
	defer srv.Close()

	st := invoke(newServer(t, srv.URL), map[string]any{"body": "x", "credentialMount": "cred/webhook"},
		&pluginv1.CredentialRef{Name: "cred/webhook"}) // NAME only, coordinates withheld
	term := st.terminal()
	if term == nil || term.GetOk() {
		t.Fatalf("withheld coordinates must fail closed (terminal not-ok), got %+v", term)
	}
	if posted {
		t.Fatal("no POST may be attempted when the credential cannot be resolved")
	}
}
