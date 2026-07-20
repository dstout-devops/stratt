package adoptplugin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dstout-devops/stratt/core/internal/awximport/awx/awxsim"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeStream captures the InvokeResponses the plugin streams (the notify test pattern).
type fakeStream struct {
	grpc.ServerStreamingServer[pluginv1.InvokeResponse]
	ctx  context.Context
	sent []*pluginv1.InvokeResponse
}

func (f *fakeStream) Send(r *pluginv1.InvokeResponse) error { f.sent = append(f.sent, r); return nil }
func (f *fakeStream) Context() context.Context              { return f.ctx }

func (f *fakeStream) terminal() (*pluginv1.TaskEvent, *pluginv1.InvokeResult) {
	for _, r := range f.sent {
		if ev := r.GetEvent(); ev.GetTerminal() {
			return ev, r.GetResult()
		}
	}
	return nil, nil
}

func newServer(t *testing.T) *Server {
	t.Helper()
	// The AWX CredentialRef material lives ONLY in the K8s Secret the SecretBroker reads
	// in-pod (§2.5) — never in the args, never in the core.
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "stratt-secrets", Name: "awx-token"},
		Data:       map[string][]byte{"token": []byte("s3cr3t")},
	})
	return New("adopt", secretbroker.New(cs, "stratt-secrets"), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func awxCredRef() *pluginv1.CredentialRef {
	return &pluginv1.CredentialRef{
		Name: "cred/awx",
		Resolved: &pluginv1.ResolvedRef{
			SecretNamespace: "stratt-secrets", SecretName: "awx-token",
			Keys: []*pluginv1.ResolvedKey{{Key: "token", Name: "token"}},
		},
	}
}

func invoke(s *Server, args materializeArgs, creds ...*pluginv1.CredentialRef) *fakeStream {
	raw, _ := json.Marshal(args)
	st := &fakeStream{ctx: context.Background()}
	_ = s.Invoke(&pluginv1.InvokeRequest{
		Envelope: &pluginv1.Envelope{Creds: creds},
		Args:     &pluginv1.Payload{Bytes: raw},
		Action:   actionMaterialize,
	}, st)
	return st
}

// TestInvoke_MaterializesWithResolvedCredential is the whole pod-side path (ADR-0088): the
// SecretBroker resolves the AWX token from the core-provided COORDINATES, the plugin builds
// the reader, does the targeted deep-read against awxsim, runs the transform, and returns the
// reviewable bundle on InvokeResult.Outputs — carrying the cutover guard from the passed live
// set. Material never appears in the args.
func TestInvoke_MaterializesWithResolvedCredential(t *testing.T) {
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()
	sim.SetBase(srv.URL)

	st := invoke(newServer(t), materializeArgs{
		Kind: "ansible.template", Identity: "ctrl-a/10", Endpoint: srv.URL,
		NativeID: 10, Source: "ctrl-a", Live: []string{"Nightly Deploy"}, CredentialMount: "cred/awx",
	}, awxCredRef())

	ev, res := st.terminal()
	if ev == nil || !ev.GetOk() {
		t.Fatalf("expected a green terminal, got %+v", ev)
	}
	if res.GetOutputContract().GetSchemaId() != outputContract {
		t.Fatalf("output contract id = %q, want %q", res.GetOutputContract().GetSchemaId(), outputContract)
	}
	var out materializeOutput
	if err := json.Unmarshal(res.GetOutputs().GetBytes(), &out); err != nil {
		t.Fatalf("decode outputs: %v", err)
	}
	var wf string
	for path := range out.Files {
		if strings.HasPrefix(path, "workflows/") {
			wf = path
		}
	}
	if wf == "" {
		t.Fatalf("expected a workflow file; got %v", out.Files)
	}
	if !strings.Contains(out.Files[wf], "adoptedFrom:") || !strings.Contains(out.Files[wf], "ctrl-a/10") {
		t.Fatalf("workflow missing adopted-from lineage:\n%s", out.Files[wf])
	}
	// The cutover guard names the still-live schedule that was resolved core-side and passed in.
	if !strings.Contains(out.Report, "Cutover") || !strings.Contains(out.Report, "Nightly Deploy") {
		t.Fatalf("report missing cutover guard / live schedule:\n%s", out.Report)
	}
}

// TestInvoke_FailsClosedWithoutCredential: no matching credential ⇒ a failed terminal, no
// bundle. The material chokepoint is the SecretBroker; a withheld ref never yields a read.
func TestInvoke_FailsClosedWithoutCredential(t *testing.T) {
	st := invoke(newServer(t), materializeArgs{
		Kind: "ansible.template", Identity: "ctrl-a/10", Endpoint: "http://unused",
		NativeID: 10, Source: "ctrl-a", CredentialMount: "cred/awx",
	}) // no creds attached

	ev, res := st.terminal()
	if ev == nil || ev.GetOk() {
		t.Fatalf("expected a failed terminal, got %+v", ev)
	}
	if res.GetOutputs() != nil {
		t.Fatal("a failed adopt must carry no bundle")
	}
}
