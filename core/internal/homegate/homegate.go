// Package homegate is the Connector home-ownership supervisor (ADR-0045): it makes
// a cross-Cell Source re-home a fully automatic destination-side cutover with no
// manual Connector redeploy. A Connector deployed on a Cell that does NOT yet home
// its Source stands by — it neither claims the Source nor pulls the external system
// of record — and auto-activates the moment a fenced re-home hands the Source to
// this Cell (ADR-0044 slice 7 flips graph.source.cell here; the DB home gate,
// migration 00032, is the single-writer backstop underneath).
//
// The single-writer GUARANTEE lives in the data layer (the seal fence + home gate
// triggers); this package is the graceful-standby UX + observability on top: it
// keeps a standby Connector off the external SoR (no enumerate-then-drop), surfaces
// a stuck/uncertain standby as a Finding, and detects a greenfield home-collision.
package homegate

import (
	"context"
	"log/slog"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// HomeState is where a Source lives relative to THIS daemon's Cell.
type HomeState string

const (
	// Active — this Cell homes the Source and it is not sealed: project it.
	Active HomeState = "active"
	// Standby — a named PEER Cell homes the Source: do not claim or project;
	// wait for a re-home to hand it here.
	Standby HomeState = "standby"
	// Sealed — this Cell homes the Source but it is mid cross-Cell re-home
	// (rehoming_to set): stop projecting until the move completes/aborts.
	Sealed HomeState = "sealed"
	// Greenfield — no Cell (local or peer) homes the Source yet: claim it.
	Greenfield HomeState = "greenfield"
	// Uncertain — the fleet home could not be resolved (a peer is unreachable):
	// stand by CONSERVATIVELY rather than risk stealing a peer's Source, and
	// surface it (a stuck standby is never silent, §1.8).
	Uncertain HomeState = "uncertain"
	// Degraded — this Cell homes the Source but the claim (RegisterSource / owner
	// registration) keeps failing, so it is NOT projecting. Reported instead of a
	// misleading "active", and surfaced as a Finding — the moved claim must never
	// log-loop invisibly (§1.8).
	Degraded HomeState = "degraded"
)

// Home is the resolved fleet placement of a Source.
type Home struct {
	State HomeState
	// Cell is the homing Cell (this daemon for Active/Sealed, the peer for
	// Standby, empty for Greenfield/Uncertain).
	Cell string
}

// Projectable reports whether this daemon should sync+project the Source now.
func (h Home) Projectable() bool { return h.State == Active || h.State == Greenfield }

// LocalStore is the subset of graph.Store the resolver reads locally.
type LocalStore interface {
	GetSourceHome(ctx context.Context, name string) (cell string, rehomingTo string, found bool, err error)
	PeerCells(ctx context.Context) ([]types.Cell, error)
}

// Prober asks ONE peer Cell whether it homes a Source, returning the peer's home
// Cell, whether it homes it, and whether that home is SEALED (mid re-home).
// Injected so the supervisor is unit-testable without HTTP; in production it is an
// HMAC-signed GET /sources/{name} to the peer. A peer that homes a Source — sealed
// or not — makes this daemon stand by (never steal); the sealed flag lets the
// collision reconcile exclude the expected brief re-home overlap.
type Prober func(ctx context.Context, endpoint, name string) (homeCell string, homed, sealed bool, err error)

// Resolver resolves a Source's fleet home: local first (authoritative for a
// locally-homed Source — single-writer), then the peers.
type Resolver struct {
	Cell  string
	Store LocalStore
	Probe Prober
}

// Resolve determines where the Source lives across the fleet.
func (r *Resolver) Resolve(ctx context.Context, name string) Home {
	cell, rehoming, found, err := r.Store.GetSourceHome(ctx, name)
	if err == nil && found {
		switch {
		case rehoming != "":
			return Home{State: Sealed, Cell: r.Cell}
		case cell == r.Cell || cell == "" || cell == types.LocalCell:
			// Homed here (or an unclaimed/local row this daemon owns).
			return Home{State: Active, Cell: r.Cell}
		default:
			// A cached row naming a peer as home (standby residue).
			return Home{State: Standby, Cell: cell}
		}
	}
	// Not homed locally — ask the peers. A single-Cell estate (no peers) short-
	// circuits to greenfield: byte-identical to the pre-ADR-0045 always-claim.
	peers, err := r.Store.PeerCells(ctx)
	if err != nil {
		return Home{State: Uncertain}
	}
	unreachable := false
	for _, p := range peers {
		home, homed, _, err := r.Probe(ctx, p.Endpoint, name)
		if err != nil {
			unreachable = true
			continue
		}
		if homed {
			c := home
			if c == "" {
				c = p.Name
			}
			return Home{State: Standby, Cell: c}
		}
	}
	if unreachable {
		// A peer we could not reach MIGHT home it — do not claim (never steal).
		return Home{State: Uncertain}
	}
	return Home{State: Greenfield}
}

// Deps are the supervisor's collaborators.
type Deps struct {
	Resolver *Resolver
	Status   *Status
	// OpenStandbyFinding/ResolveStandbyFinding surface a stuck/uncertain standby
	// (§1.8 must-fix 4). No-op-safe (nil) for tests.
	OpenStandbyFinding    func(ctx context.Context, source, reason string) error
	ResolveStandbyFinding func(ctx context.Context, source string) error
	Log                   *slog.Logger
	// Poll is how often a standby re-checks the fleet home. Defaults to 15s.
	Poll time.Duration
}

// Supervise runs a Connector under home-ownership control (ADR-0045 must-fix 6):
// it calls register+run ONLY while this Cell should project the Source, and
// otherwise stands by (no external SoR load) polling for a re-home to hand it
// here. register claims the Source + owner registrations; run is the Syncer loop.
// Both block/return on ctx; run also returns when the seal fence rejects a write
// after the Source is sealed out from under it, which re-loops to standby.
func Supervise(ctx context.Context, d Deps, source string, register, run func(context.Context) error) {
	poll := d.Poll
	if poll <= 0 {
		poll = 15 * time.Second
	}
	for ctx.Err() == nil {
		home := d.Resolver.Resolve(ctx, source)
		d.Status.Set(source, home)

		if home.Projectable() {
			if err := register(ctx); err != nil {
				// A stuck claim (owner conflict, DB perms) must not silently report
				// "active" while nothing projects (§1.8): reflect a DEGRADED status
				// and surface a Finding, not just a log line.
				d.Status.Set(source, Home{State: Degraded, Cell: home.Cell})
				d.openStandby(ctx, source, "claim failed: "+err.Error())
				d.logf("home-supervisor: register failed; retrying", source, err)
				if !sleep(ctx, poll) {
					return
				}
				continue
			}
			d.resolveStandby(ctx, source) // claimed cleanly → clear any standby/degraded Finding
			// Run holds until ctx ends or the Source is sealed/re-homed away
			// (the seal/home gate then errors its writes). Either way, re-loop.
			if err := run(ctx); err != nil && ctx.Err() == nil {
				d.logf("home-supervisor: syncer returned; re-evaluating home", source, err)
			}
			continue
		}

		// Standby. A resolvable peer-home is expected/quiet; an Uncertain home is
		// a stuck standby and MUST surface (§1.8).
		if home.State == Uncertain {
			d.openStandby(ctx, source, "fleet home unresolved (a peer Cell is unreachable); standing by rather than risk a double-writer")
		} else {
			d.resolveStandby(ctx, source)
		}
		if !sleep(ctx, poll) {
			return
		}
	}
}

func (d Deps) resolveStandby(ctx context.Context, source string) {
	if d.ResolveStandbyFinding != nil {
		if err := d.ResolveStandbyFinding(ctx, source); err != nil {
			d.logf("home-supervisor: resolve standby finding failed", source, err)
		}
	}
}

func (d Deps) openStandby(ctx context.Context, source, reason string) {
	if d.OpenStandbyFinding != nil {
		if err := d.OpenStandbyFinding(ctx, source, reason); err != nil {
			d.logf("home-supervisor: open standby finding failed", source, err)
		}
	}
}

func (d Deps) logf(msg, source string, err error) {
	if d.Log != nil {
		d.Log.Warn(msg, "source", source, "err", err)
	}
}

// sleep waits d or until ctx ends; returns false if ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
