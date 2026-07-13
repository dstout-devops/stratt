package certissuer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Syncer is this Connector's projection capability (§2.2). The CLM has no
// change feed, so each cycle is an honest full enumeration + tombstone-absent
// — recorded, not hidden (the awsec2 posture). Revoked certs are treated as
// absent, so a revoke/renew reflects in the graph and its Findings resolve.
type Syncer struct {
	cfg      Config
	store    *graph.Store
	log      *slog.Logger
	source   types.Source
	interval time.Duration
	client   *Client
}

// NewSyncer prepares a Syncer for one registered CLM Source.
func NewSyncer(cfg Config, interval time.Duration, store *graph.Store, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Syncer{
		cfg:      cfg,
		store:    store,
		log:      log.With("connector", "certissuer", "source", cfg.SourceName),
		interval: interval,
	}
}

// Register records the Source and claims this Syncer's Facet namespaces in the
// ownership registry (§2.1) — registration precedes any write.
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "certissuer",
		Name:     s.cfg.SourceName,
		Endpoint: s.cfg.Addr,
	})
	if err != nil {
		return err
	}
	s.source = src
	for _, o := range s.cfg.FacetNamespaces() {
		if err := s.store.RegisterFacetOwner(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) provenance() types.Provenance {
	return types.Provenance{
		WriterKind: types.WriterSyncer,
		WriterRef:  s.cfg.SyncerRef(),
		SourceID:   s.source.ID,
		At:         time.Now().UTC(),
	}
}

// Run polls full enumerations until ctx ends.
func (s *Syncer) Run(ctx context.Context) error {
	s.client = NewClient(s.cfg.Addr, s.cfg.Token, s.cfg.Mount)
	for {
		if err := s.Sync(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log.Error("sync cycle failed; retrying next interval", "error", err)
		}
		select {
		case <-time.After(s.interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Sync enumerates every issued cert and projects the live leaf certs. The CA
// and revoked certs are skipped, so they tombstone the same cycle they stop
// being live estate certs (§1.2: the graph reflects the CLM, never invents).
func (s *Syncer) Sync(ctx context.Context) error {
	started := time.Now()
	prov := s.provenance()
	projector := s.store.NormalizerProjector()
	seen := []string{}
	projected := 0

	serials, err := s.client.ListSerials(ctx)
	if err != nil {
		return err
	}
	for _, serial := range serials {
		crt, err := s.client.GetCert(ctx, serial)
		if err != nil {
			s.log.Warn("skipping cert (read failed)", "serial", serial, "error", err)
			continue
		}
		if crt.Revoked {
			continue // revoked = absent; the tombstone pass removes any prior Entity
		}
		up, ok, err := normalizeCert(crt)
		if err != nil {
			s.log.Warn("skipping cert", "serial", serial, "error", err)
			continue
		}
		if !ok {
			continue // CA / non-leaf
		}
		if _, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up}); err != nil {
			if errors.Is(err, graph.ErrIdentityConflict) {
				s.log.Error("identity conflict; not merging (§1.2)", "serial", serial, "error", err)
				continue
			}
			return err
		}
		projected++
		seen = append(seen, serial)
	}

	if _, err := projector.TombstoneAbsent(ctx, prov, "cert.serial", seen); err != nil {
		return err
	}
	if err := s.store.SetSyncCursor(ctx, s.source.ID, "", true); err != nil {
		return err
	}
	s.log.Info("full sync complete", "certs", projected, "took", time.Since(started).String())
	return nil
}
