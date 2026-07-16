package homegate

import (
	"context"
	"sort"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// CollisionStore is the subset of graph.Store the collision reconcile needs: the
// local Sources homed here (unsealed), and the Finding writer/resolver.
type CollisionStore interface {
	// LocalHomedSources returns the names of Sources this Cell homes and are NOT
	// sealed for re-home (a sealed Source's brief cross-Cell overlap is expected).
	LocalHomedSources(ctx context.Context, cell string) ([]string, error)
	PeerCells(ctx context.Context) ([]types.Cell, error)
	WriteSourceCollisionFinding(ctx context.Context, source string, cells []string) error
	ResolveSourceCollisionFinding(ctx context.Context, source string) error
}

// Reconciler is the periodic home-ownership reconcile (ADR-0045 must-fix 2): it
// raises a CRITICAL Finding when more than one Cell homes the same Source NAME
// with neither sealed — the "greenfield simultaneous-claim" double-writer the
// slice-2 placement Finding cannot see (that check is per-Cell; entity ids even
// differ across Cells). It NEVER silently picks a winner (§2.4 anti-GPO): the
// collision is surfaced for deliberate, fenced resolution (a re-home).
type Reconciler struct {
	Cell  string
	Store CollisionStore
	Probe Prober
	// Interval defaults to 60s.
	Interval time.Duration
}

// Run sweeps until ctx ends. Leader-gated by the caller (one writer of Findings).
func (rc *Reconciler) Run(ctx context.Context) error {
	interval := rc.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	rc.sweep(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			rc.sweep(ctx)
		}
	}
}

func (rc *Reconciler) sweep(ctx context.Context) {
	names, err := rc.Store.LocalHomedSources(ctx, rc.Cell)
	if err != nil {
		return
	}
	peers, err := rc.Store.PeerCells(ctx)
	if err != nil || len(peers) == 0 {
		return // single-Cell estate: no cross-Cell collision possible
	}
	for _, name := range names {
		var colliding []string
		for _, p := range peers {
			home, homed, sealed, err := rc.Probe(ctx, p.Endpoint, name)
			if err != nil {
				continue // an unreachable peer is not evidence of a collision
			}
			// A peer that homes the SAME Source name UNSEALED is a genuine
			// second writer. A sealed peer-home is the expected re-home overlap.
			if homed && !sealed {
				c := home
				if c == "" {
					c = p.Name
				}
				colliding = append(colliding, c)
			}
		}
		if len(colliding) > 0 {
			cells := append([]string{rc.Cell}, colliding...)
			sort.Strings(cells)
			_ = rc.Store.WriteSourceCollisionFinding(ctx, name, cells)
		} else {
			_ = rc.Store.ResolveSourceCollisionFinding(ctx, name)
		}
	}
}
