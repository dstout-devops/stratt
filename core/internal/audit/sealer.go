package audit

import (
	"context"
	"log/slog"
	"time"
)

// SealStore is the store surface the sealer drives — implemented by
// graph.Store. Keeping it an interface here avoids an import cycle (the store
// imports this package for ChainHash) and makes the loop testable with a fake.
type SealStore interface {
	// SealPending chains the unsealed tail in seq order and returns how many
	// events it sealed this pass.
	SealPending(ctx context.Context) (int, error)
}

// Sealer is the single-writer controller that continuously chains the
// append-only audit ledger (ADR-0034), a structural twin of the baseline/
// trigger reconcilers. Sealing is decoupled from the hot-path append so
// integrity never bottlenecks the full access log; the unsealed tail stays
// small (Interval, ~1s). It is the only writer of prev_hash/hash.
type Sealer struct {
	Store    SealStore
	Interval time.Duration
	Log      *slog.Logger
}

// Run seals on a ticker until ctx is cancelled. A sealing error is logged and
// retried next tick — the unsealed events remain, so nothing is lost.
func (s *Sealer) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.Store.SealPending(ctx)
			if err != nil {
				if s.Log != nil {
					s.Log.Error("audit sealer", "err", err)
				}
				continue
			}
			if n > 0 && s.Log != nil {
				s.Log.Debug("audit sealer", "sealed", n)
			}
		}
	}
}
