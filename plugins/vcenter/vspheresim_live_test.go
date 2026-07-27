package vcenter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The whole provision→observe→reach chain against vspheresim, through the SAME
// Action the estate launches and with ordinary vSphere params only.
//
// This is the test the stock simulator could not support. vcsim executes create-vm
// and the inventory is real, but no guest OS boots — so `mgmt.address` was never
// produced and provision→configure could not be exercised end to end on vSphere at
// all. With a container-backed guest it can, and the assertion below is that the
// coordinate comes back as a NAME (ADR-0143 D1), which is only reachable because the
// simulator gives its guests a dotted hostname.
//
// Gated on VSPHERESIM_URL, so it is a no-op in normal `task ci`. Run it with:
//
//	task dev:vspheresim:up
//	VSPHERESIM_URL=http://127.0.0.1:8989/sdk go test ./ -run LiveVsphereSim -v
func TestLiveVsphereSim(t *testing.T) {
	url := os.Getenv("VSPHERESIM_URL")
	if url == "" {
		t.Skip("set VSPHERESIM_URL to run the vspheresim provision→reach proof (task dev:vspheresim:up)")
	}
	srv := NewServer(Config{Endpoint: url, Username: "user", Password: "pass", Insecure: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Create a VM through the SAME Action the estate launches — ordinary vSphere params,
	// nothing simulator-specific.
	args, _ := json.Marshal(map[string]any{"name": "web-01", "cpus": 2, "memoryMB": 2048})
	st := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := srv.Invoke(&pluginv1.InvokeRequest{Action: "vcenter/create-vm", Args: &pluginv1.Payload{Bytes: args}}, st); err != nil {
		t.Fatalf("create-vm: %v", err)
	}
	term := st.sent[len(st.sent)-1]
	if !term.GetEvent().GetOk() {
		t.Fatalf("create-vm not ok: %s", term.GetEvent().GetMessage())
	}
	t.Logf("VM created")

	// Poll Observe until the guest appears — modelling exactly what a client must do
	// while a machine boots.
	deadline := time.Now().Add(60 * time.Second)
	for {
		obs := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
		if err := srv.Observe(&pluginv1.ObserveRequest{}, obs); err != nil {
			t.Fatalf("observe: %v", err)
		}
		for _, r := range obs.sent {
			for _, e := range r.GetEntities() {
				if e.GetKind() != "vm" {
					continue
				}
				if raw := e.GetFacets()["mgmt.address"]; len(raw) > 0 {
					var m map[string]any
					_ = json.Unmarshal(raw, &m)
					addr, _ := m["address"].(string)
					t.Logf("guest reachable: vm=%s mgmt.address=%s", e.GetLabels()["vcenter.name"], addr)
					// A NAME, not an address. If this ever reports an IP it means the
					// simulator stopped giving guests a dotted hostname, and the
					// name-first branch of reachCoordinate has silently stopped being
					// exercised anywhere (ADR-0143 D1).
					if !strings.Contains(addr, ".") || net.ParseIP(addr) != nil {
						t.Fatalf("mgmt.address = %q; want a dotted NAME — the name-first path is no longer covered", addr)
					}
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no VM ever reported a guest coordinate — check the simulator log for a dead-guest warning")
		}
		time.Sleep(2 * time.Second)
	}
}

type captureStream[T any] struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*T
}

func (s *captureStream[T]) Context() context.Context { return s.ctx }
func (s *captureStream[T]) Send(m *T) error          { s.sent = append(s.sent, m); return nil }
