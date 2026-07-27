package vcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
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
// all. With a container-backed guest it can, and the assertion is that the
// coordinate comes back as a NAME (ADR-0143 D1), which is only reachable because the
// simulator gives its guests a dotted hostname.
//
// Gated on VSPHERESIM_URL, so it is a no-op in normal `task ci`. Run it with:
//
//	task dev:vspheresim:up
//	task dev:vspheresim:proof
func TestLiveVsphereSim(t *testing.T) {
	provisionAndAwaitCoordinate(t, liveServer(t), uniqueVMName("web"))
}

// And then USE the coordinate. The published address being a name is worth nothing
// on its own — what makes the vSphere leg converge-able is that the name resolves,
// the guest answers, and a command runs on it.
//
// The reach runs from a PEER CONTAINER on the guest network, which is not a
// convenience: docker's embedded DNS serves only containers attached to the network,
// so that is the only place the published name resolves. This is the harness being
// honest about a real property — a name coordinate is usable exactly where a
// resolver for it exists, which in an estate is DNS and here is docker. Reaching by
// address from outside would pass while proving the opposite of what is claimed.
//
// WHAT THIS DOES NOT PROVE: that an EE Job running in kind can reach a guest on the
// host's docker daemon. That is PLG-1, it is a harness-topology problem rather than
// a vSphere one, and it is not fixed by anything here.
func TestLiveVsphereSimConverge(t *testing.T) {
	srv := liveServer(t)
	network, image, key := os.Getenv("VSPHERESIM_GUEST_NETWORK"), os.Getenv("VSPHERESIM_GUEST_IMAGE"), os.Getenv("VSPHERESIM_GUEST_KEY")
	if network == "" || image == "" || key == "" {
		t.Skip("set VSPHERESIM_GUEST_NETWORK, VSPHERESIM_GUEST_IMAGE and VSPHERESIM_GUEST_KEY to run the converge proof (task dev:vspheresim:proof sets all three)")
	}

	addr := provisionAndAwaitCoordinate(t, srv, uniqueVMName("web"))

	// A machine boots before it listens, so the coordinate appearing does not mean
	// sshd is up. Retrying is what a converge does too.
	var out string
	deadline := time.Now().Add(90 * time.Second)
	for {
		var err error
		if out, err = sshExec(network, image, key, addr, "hostname -f; python3 -V; id -un"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never executed on %s: %v\n%s", addr, err, out)
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("executed on %s:\n%s", addr, strings.TrimSpace(out))

	// The command ran as the credentialed user on the host the coordinate names —
	// the three facts that separate "reachable" from "converge-able".
	if !strings.Contains(out, addr) {
		t.Errorf("the guest reports a different name than the coordinate reached it by: want %q in\n%s", addr, out)
	}
	if !strings.Contains(out, "root") {
		t.Errorf("the dev machine credential did not authenticate as root:\n%s", out)
	}
	// Python is the far half of a configuration tool. Without it the host is
	// reachable and still not converge-able, which is exactly the distinction this
	// test exists to hold.
	if !strings.Contains(out, "Python 3") {
		t.Errorf("no python3 on the guest — reachable but not converge-able:\n%s", out)
	}
}

// uniqueVMName keeps each run's VM distinct from every previous run's.
//
// A fixed name is a quiet way to make this suite vacuous: vSphere is happy to hold
// two VMs with the same name, so a leftover guest from an earlier run satisfies the
// Observe poll instantly — the test then reaches a machine it did not create, and
// passes without the provisioning path ever having produced anything. The proof is
// meant to be of THIS run's VM.
//
// Still an RFC 1123 label, because the simulator refuses to give a hostname to a
// name that is not one and the coordinate would silently fall back to an address.
func uniqueVMName(prefix string) string {
	return fmt.Sprintf("%s-%x", prefix, time.Now().UnixNano()&0xffffff)
}

func liveServer(t *testing.T) *Server {
	t.Helper()
	url := os.Getenv("VSPHERESIM_URL")
	if url == "" {
		t.Skip("set VSPHERESIM_URL to run the vspheresim proof (task dev:vspheresim:up)")
	}
	return NewServer(Config{Endpoint: url, Username: "user", Password: "pass", Insecure: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// provisionAndAwaitCoordinate creates a VM through the real Action and returns the
// coordinate the Syncer projects for it, once the guest reports one.
func provisionAndAwaitCoordinate(t *testing.T, srv *Server, name string) string {
	t.Helper()
	// Ordinary vSphere params, nothing simulator-specific — the point of the exercise.
	args, _ := json.Marshal(map[string]any{"name": name, "cpus": 2, "memoryMB": 2048})
	st := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := srv.Invoke(&pluginv1.InvokeRequest{Action: "vcenter/create-vm", Args: &pluginv1.Payload{Bytes: args}}, st); err != nil {
		t.Fatalf("create-vm: %v", err)
	}
	term := st.sent[len(st.sent)-1]
	if !term.GetEvent().GetOk() {
		t.Fatalf("create-vm not ok: %s", term.GetEvent().GetMessage())
	}
	t.Logf("VM %s created", name)

	// Poll Observe until the guest appears — exactly what a client must do while a
	// machine boots.
	deadline := time.Now().Add(60 * time.Second)
	for {
		obs := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
		if err := srv.Observe(&pluginv1.ObserveRequest{}, obs); err != nil {
			t.Fatalf("observe: %v", err)
		}
		for _, r := range obs.sent {
			for _, e := range r.GetEntities() {
				if e.GetKind() != "vm" || e.GetLabels()["vcenter.name"] != name {
					continue
				}
				raw := e.GetFacets()["mgmt.address"]
				if len(raw) == 0 {
					continue
				}
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				addr, _ := m["address"].(string)
				t.Logf("guest reachable: vm=%s mgmt.address=%s", name, addr)
				// A NAME, not an address. If this ever reports an IP it means the
				// simulator stopped giving guests a dotted hostname, and the name-first
				// branch of reachCoordinate has silently stopped being exercised
				// anywhere (ADR-0143 D1).
				if !strings.Contains(addr, ".") || net.ParseIP(addr) != nil {
					t.Fatalf("mgmt.address = %q; want a dotted NAME — the name-first path is no longer covered", addr)
				}
				return addr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("VM %s never reported a guest coordinate — check the simulator log for a dead-guest warning", name)
		}
		time.Sleep(2 * time.Second)
	}
}

// sshExec runs remote on the guest named by addr, from a peer container on the guest
// network.
//
// addr and remote are passed as POSITIONAL ARGUMENTS to sh, never interpolated into
// the script. addr arrives from the graph and the graph got it from a VM name a
// client chose, so it is untrusted all the way down; as an argument it is a value
// that cannot become code no matter what it contains.
func sshExec(network, image, key, addr, remote string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--network", network,
		"-v", key+":/key:ro",
		// The guest image doubles as the client: it carries the openssh client, so the
		// far end of the connection needs no second image.
		"--entrypoint", "sh", image,
		// The key is copied because ssh refuses a world-readable private key and the
		// mount is read-only.
		// LogLevel=ERROR is an assertion aid, not noise reduction: ssh's
		// "Permanently added <host>" notice echoes the address into the captured
		// output, so a caller checking that the guest reported its own name would
		// match ssh's chatter and pass even when the remote command produced nothing.
		"-c", `cp /key /k && chmod 600 /k && exec ssh -i /k `+
			`-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR `+
			`-o BatchMode=yes -o ConnectTimeout=5 "root@$1" "$2"`,
		"vspheresim-reach", addr, remote).CombinedOutput()
	return string(out), err
}

type captureStream[T any] struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*T
}

func (s *captureStream[T]) Context() context.Context { return s.ctx }
func (s *captureStream[T]) Send(m *T) error          { s.sent = append(s.sent, m); return nil }
