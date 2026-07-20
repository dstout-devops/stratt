package awx

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

	"github.com/dstout-devops/stratt/plugins/awx/controller/awxsim"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

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

func adoptServer(t *testing.T) *Server {
	t.Helper()
	// The AWX token lives ONLY in the K8s Secret the SecretBroker reads in-pod (§2.5).
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "stratt-secrets", Name: "awx-token"},
		Data:       map[string][]byte{"token": []byte("s3cr3t")},
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(ServerConfig{}, nil, secretbroker.New(cs, "stratt-secrets"), log)
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

func invokeAdopt(s *Server, args materializeArgs, creds ...*pluginv1.CredentialRef) *fakeStream {
	raw, _ := json.Marshal(args)
	st := &fakeStream{ctx: context.Background()}
	_ = s.Invoke(&pluginv1.InvokeRequest{
		Envelope: &pluginv1.Envelope{Creds: creds},
		Args:     &pluginv1.Payload{Bytes: raw},
		Action:   actionMaterialize,
	}, st)
	return st
}

// TestInvoke_MaterializesWithResolvedCredential is the whole plugin-side adopt path (ADR-0089):
// the SecretBroker resolves the AWX token from the core-provided COORDINATES, the plugin builds
// the controller client, deep-reads awxsim, runs the transform, and returns the bundle on
// InvokeResult.Outputs — with the cutover guard from the passed live set. Material never appears
// in the args.
func TestInvoke_MaterializesWithResolvedCredential(t *testing.T) {
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()
	sim.SetBase(srv.URL)

	st := invokeAdopt(adoptServer(t), materializeArgs{
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
	if !strings.Contains(out.Report, "Cutover") || !strings.Contains(out.Report, "Nightly Deploy") {
		t.Fatalf("report missing cutover guard / live schedule:\n%s", out.Report)
	}
}

// TestInvoke_FailsClosedWithoutCredential: no matching credential ⇒ a failed terminal, no
// bundle. The material chokepoint is the SecretBroker; a withheld ref never yields a read.
func TestInvoke_FailsClosedWithoutCredential(t *testing.T) {
	st := invokeAdopt(adoptServer(t), materializeArgs{
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
