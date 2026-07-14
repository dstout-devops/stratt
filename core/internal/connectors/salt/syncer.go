package salt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Syncer is the Connector's projection capability (§2.2): Salt minion grains
// flowing through the Normalizer into the graph with Provenance.
type Syncer struct {
	cfg      Config
	store    *graph.Store
	log      *slog.Logger
	source   types.Source
	interval time.Duration
	client   *saltClient
}

// NewSyncer prepares a Syncer for one registered Source.
func NewSyncer(cfg Config, interval time.Duration, store *graph.Store, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Syncer{
		cfg:      cfg,
		store:    store,
		log:      log.With("connector", "salt", "source", cfg.SourceName),
		interval: interval,
	}
}

// Register records the Source and claims this Syncer's Facet namespaces (§2.1).
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "salt",
		Name:     s.cfg.SourceName,
		Endpoint: s.cfg.APIURL,
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

// Run enumerates minion grains on an interval until ctx ends. Salt has no grain
// change feed, so every cycle is a full enumeration that also tombstones the
// minions the master no longer reports — never silent data loss.
func (s *Syncer) Run(ctx context.Context) error {
	s.client = newSaltClient(s.cfg)
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

// Sync runs one full enumeration via the runner cache.grains (the master's
// grain cache — no minion round-trip, immune to dead minions; the cache may be
// stale, an acceptable trade for a rebuildable read-model, §1.2). A single
// minion that fails to normalize is skipped, not fatal (§1.8).
func (s *Syncer) Sync(ctx context.Context) error {
	started := time.Now()
	grainsByMinion, err := s.cacheGrains(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(grainsByMinion))
	for id := range grainsByMinion {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic order for logs/tests

	prov := s.provenance()
	projector := s.store.NormalizerProjector()
	seen := make([]string, 0, len(ids))
	projected := 0

	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		up, err := normalizeMinion(id, grainsByMinion[id])
		if err != nil {
			s.log.Warn("skipping minion", "minion", id, "error", err)
			continue
		}
		if _, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up}); err != nil {
			if errors.Is(err, graph.ErrIdentityConflict) {
				s.log.Error("identity conflict; not merging (§1.2)", "minion", id, "error", err)
				continue
			}
			return err
		}
		projected++
		seen = append(seen, id)
	}

	removed, err := projector.TombstoneAbsent(ctx, prov, "salt.minion_id", seen)
	if err != nil {
		return err
	}
	s.log.Info("salt sync complete", "minions", projected, "removed", removed, "took", time.Since(started).String())
	return nil
}

// cacheGrains calls the runner cache.grains and returns minion-id -> grains.
// tgt is mandatory since Salt 3001, so "*" is always sent.
func (s *Syncer) cacheGrains(ctx context.Context) (map[string]map[string]any, error) {
	token, err := s.client.authToken(ctx)
	if err != nil {
		return nil, err
	}
	lowstate, _ := json.Marshal(map[string]any{
		"client":   "runner",
		"fun":      "cache.grains",
		"tgt":      "*",
		"tgt_type": "glob",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.APIURL+"/", bytes.NewReader(lowstate))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Auth-Token", token)

	res, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("salt: cache.grains request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusUnauthorized {
		// Token expired — drop it so the next cycle re-logs-in.
		s.client.mu.Lock()
		s.client.token = ""
		s.client.mu.Unlock()
		return nil, fmt.Errorf("salt: cache.grains: unauthorized (token cleared for re-login)")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("salt: cache.grains: %s", res.Status)
	}
	var out struct {
		Return []map[string]map[string]any `json:"return"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("salt: decode cache.grains: %w", err)
	}
	if len(out.Return) == 0 {
		return map[string]map[string]any{}, nil
	}
	return out.Return[0], nil
}
