package chef

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	chefapi "github.com/go-chef/chef"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Syncer is this Connector's projection capability (§2.2): Chef node objects
// flowing through the Normalizer into the graph with Provenance.
type Syncer struct {
	cfg      Config
	store    *graph.Store
	log      *slog.Logger
	source   types.Source
	interval time.Duration
	client   *chefapi.Client
}

// NewSyncer prepares a Syncer for one registered Source.
func NewSyncer(cfg Config, interval time.Duration, store *graph.Store, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Syncer{
		cfg:      cfg,
		store:    store,
		log:      log.With("connector", "chef", "source", cfg.SourceName),
		interval: interval,
	}
}

// Register records the Source and claims this Syncer's Facet namespaces in the
// ownership registry (§2.1) — registration precedes any write.
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "chef",
		Name:     s.cfg.SourceName,
		Endpoint: s.cfg.ServerURL,
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

// Run enumerates the Chef estate on an interval until ctx ends. Chef has no
// change feed, so every cycle is a full enumeration that also tombstones the
// nodes this Source no longer reports — never silent data loss.
func (s *Syncer) Run(ctx context.Context) error {
	client, err := s.cfg.chefClient()
	if err != nil {
		return err
	}
	s.client = client
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

// Sync runs one full enumeration: list every node name, fetch and normalize
// each, project it, then tombstone every chef.node.name this Source no longer
// reports. A single node that fails to fetch/normalize is skipped, not fatal —
// one bad node never blocks the estate (§1.8).
func (s *Syncer) Sync(ctx context.Context) error {
	started := time.Now()
	list, err := s.client.Nodes.List()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order for logs/tests

	prov := s.provenance()
	projector := s.store.NormalizerProjector()
	seen := make([]string, 0, len(names))
	projected := 0

	for _, name := range names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		node, err := s.client.Nodes.Get(name)
		if err != nil {
			s.log.Warn("skipping node; fetch failed", "node", name, "error", err)
			continue
		}
		up, err := normalizeNode(node)
		if err != nil {
			s.log.Warn("skipping node", "node", name, "error", err)
			continue
		}
		if _, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up}); err != nil {
			if errors.Is(err, graph.ErrIdentityConflict) {
				s.log.Error("identity conflict; not merging (§1.2)", "node", name, "error", err)
				continue
			}
			return err
		}
		projected++
		seen = append(seen, name)
	}

	removed, err := projector.TombstoneAbsent(ctx, prov, "chef.node.name", seen)
	if err != nil {
		return err
	}
	s.log.Info("chef sync complete", "nodes", projected, "removed", removed, "took", time.Since(started).String())
	return nil
}
