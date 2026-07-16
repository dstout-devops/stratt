package homegate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

type fakeStore struct {
	mu       sync.Mutex
	cell     string
	rehoming string
	found    bool
	err      error
	peers    []types.Cell
}

func (f *fakeStore) GetSourceHome(context.Context, string) (string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cell, f.rehoming, f.found, f.err
}
func (f *fakeStore) PeerCells(context.Context) ([]types.Cell, error) { return f.peers, nil }

func TestResolve(t *testing.T) {
	peer := []types.Cell{{Name: "us", Endpoint: "http://us"}}
	homedByPeer := func(context.Context, string, string) (string, bool, bool, error) { return "us", true, false, nil }
	noPeerHome := func(context.Context, string, string) (string, bool, bool, error) { return "", false, false, nil }
	peerDown := func(context.Context, string, string) (string, bool, bool, error) {
		return "", false, false, errors.New("unreachable")
	}

	cases := []struct {
		name  string
		store *fakeStore
		probe Prober
		want  HomeState
	}{
		{"homed here", &fakeStore{cell: "eu", found: true, peers: peer}, noPeerHome, Active},
		{"sealed here", &fakeStore{cell: "eu", rehoming: "us", found: true, peers: peer}, noPeerHome, Sealed},
		{"unclaimed local row", &fakeStore{cell: "local", found: true, peers: peer}, noPeerHome, Active},
		{"peer homes it", &fakeStore{found: false, peers: peer}, homedByPeer, Standby},
		{"greenfield (nobody homes it)", &fakeStore{found: false, peers: peer}, noPeerHome, Greenfield},
		{"single-cell greenfield", &fakeStore{found: false, peers: nil}, noPeerHome, Greenfield},
		{"peer unreachable → uncertain (never steal)", &fakeStore{found: false, peers: peer}, peerDown, Uncertain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Resolver{Cell: "eu", Store: c.store, Probe: c.probe}
			if got := r.Resolve(context.Background(), "src"); got.State != c.want {
				t.Fatalf("Resolve = %s, want %s", got.State, c.want)
			}
		})
	}
}

// TestSuperviseStandbyThenActivate proves the auto-cutover core: a Connector for a
// peer-homed Source STANDS BY (never registers/runs), and auto-ACTIVATES the moment
// the Source's home flips to this Cell (a re-home adopt).
func TestSuperviseStandbyThenActivate(t *testing.T) {
	// Home flips: first N resolves report peer-homed (not found locally, peer
	// homes it); after "adopt" the local row appears homed here.
	store := &fakeStore{found: false, peers: []types.Cell{{Name: "us", Endpoint: "http://us"}}}
	adopt := func() {
		store.mu.Lock()
		store.cell, store.found = "eu", true // US re-homed the Source to EU
		store.mu.Unlock()
	}
	probe := func(context.Context, string, string) (string, bool, bool, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		return "us", !store.found, false, nil // peer homes it until we're adopted
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var registered, ran bool
	var mu sync.Mutex
	register := func(context.Context) error { mu.Lock(); registered = true; mu.Unlock(); return nil }
	run := func(rc context.Context) error {
		mu.Lock()
		ran = true
		mu.Unlock()
		cancel() // activated — end the supervisor
		return nil
	}

	st := NewStatus()
	go func() {
		// Simulate the re-home landing shortly after standby begins.
		time.Sleep(30 * time.Millisecond)
		adopt()
	}()
	Supervise(ctx, Deps{
		Resolver: &Resolver{Cell: "eu", Store: store, Probe: probe},
		Status:   st, Poll: 5 * time.Millisecond,
	}, "src", register, run)

	mu.Lock()
	defer mu.Unlock()
	if !registered || !ran {
		t.Fatalf("supervisor must activate on re-home: registered=%v ran=%v", registered, ran)
	}
	if got := st.Snapshot()["src"].State; got != Active {
		t.Fatalf("final status must be Active, got %s", got)
	}
}

// TestSuperviseNeverStealsPeerHomed proves a peer-homed Source is never registered
// or run — the standby guarantee.
func TestSuperviseNeverStealsPeerHomed(t *testing.T) {
	store := &fakeStore{found: false, peers: []types.Cell{{Name: "us", Endpoint: "http://us"}}}
	probe := func(context.Context, string, string) (string, bool, bool, error) { return "us", true, false, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	var touched bool
	touch := func(context.Context) error { touched = true; return nil }

	st := NewStatus()
	Supervise(ctx, Deps{
		Resolver: &Resolver{Cell: "eu", Store: store, Probe: probe},
		Status:   st, Poll: 5 * time.Millisecond,
	}, "src", touch, touch)

	if touched {
		t.Fatal("a peer-homed Source must NEVER be registered or run (no steal)")
	}
	if got := st.Snapshot()["src"].State; got != Standby {
		t.Fatalf("status must be Standby, got %s", got)
	}
}

// TestSuperviseClaimFailedIsDegradedNotActive proves a stuck claim never reports
// a misleading "active": a persistently-failing register yields Degraded status +
// a Finding, not a silent log-loop (§1.8, charter-guardian should-fix).
func TestSuperviseClaimFailedIsDegradedNotActive(t *testing.T) {
	store := &fakeStore{cell: "eu", found: true, peers: nil} // homed here → projectable
	probe := func(context.Context, string, string) (string, bool, bool, error) { return "", false, false, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	var opened bool
	var mu sync.Mutex
	st := NewStatus()
	Supervise(ctx, Deps{
		Resolver:           &Resolver{Cell: "eu", Store: store, Probe: probe},
		Status:             st,
		OpenStandbyFinding: func(context.Context, string, string) error { mu.Lock(); opened = true; mu.Unlock(); return nil },
		Poll:               5 * time.Millisecond,
	}, "src",
		func(context.Context) error { return errors.New("owner conflict") }, // register always fails
		func(context.Context) error { return nil })

	mu.Lock()
	defer mu.Unlock()
	if !opened {
		t.Fatal("a persistently-failing claim must open a Finding")
	}
	if got := st.Snapshot()["src"].State; got != Degraded {
		t.Fatalf("a stuck claim must report Degraded, not %s", got)
	}
}

// TestSuperviseUncertainOpensFinding proves a stuck/uncertain standby (a peer is
// unreachable, so home can't be confirmed) surfaces a Finding — never silent (§1.8).
func TestSuperviseUncertainOpensFinding(t *testing.T) {
	store := &fakeStore{found: false, peers: []types.Cell{{Name: "us", Endpoint: "http://us"}}}
	probe := func(context.Context, string, string) (string, bool, bool, error) {
		return "", false, false, errors.New("unreachable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	var opened bool
	var mu sync.Mutex
	st := NewStatus()
	Supervise(ctx, Deps{
		Resolver:           &Resolver{Cell: "eu", Store: store, Probe: probe},
		Status:             st,
		OpenStandbyFinding: func(context.Context, string, string) error { mu.Lock(); opened = true; mu.Unlock(); return nil },
		Poll:               5 * time.Millisecond,
	}, "src", func(context.Context) error { return nil }, func(context.Context) error { return nil })

	mu.Lock()
	defer mu.Unlock()
	if !opened {
		t.Fatal("an uncertain (unreachable-peer) standby must open a Finding")
	}
	if got := st.Snapshot()["src"].State; got != Uncertain {
		t.Fatalf("status must be Uncertain, got %s", got)
	}
}
