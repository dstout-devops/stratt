package puppet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// defaultPageLimit is the /inventory page size — one query returns a node with
// its full fact set, so pages are cheap; 500 balances round-trips vs memory.
const defaultPageLimit = 500

// Syncer is this Connector's projection capability (§2.2): PuppetDB inventory
// entries flowing through the Normalizer into the graph with Provenance.
type Syncer struct {
	cfg       Config
	store     *graph.Store
	log       *slog.Logger
	source    types.Source
	interval  time.Duration
	pageLimit int
	client    *http.Client
}

// NewSyncer prepares a Syncer for one registered Source.
func NewSyncer(cfg Config, interval time.Duration, store *graph.Store, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Syncer{
		cfg:       cfg,
		store:     store,
		log:       log.With("connector", "puppet", "source", cfg.SourceName),
		interval:  interval,
		pageLimit: defaultPageLimit,
	}
}

// Register records the Source and claims this Syncer's Facet namespaces in the
// ownership registry (§2.1) — registration precedes any write.
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "puppet",
		Name:     s.cfg.SourceName,
		Endpoint: s.cfg.BaseURL,
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

// Run enumerates PuppetDB on an interval until ctx ends. PuppetDB has no change
// feed, so every cycle is a full enumeration that also tombstones the nodes this
// Source no longer reports — never silent data loss.
func (s *Syncer) Run(ctx context.Context) error {
	client, err := s.cfg.httpClient()
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

// Sync runs one full enumeration: page the /inventory endpoint, normalize and
// project each node, then tombstone every puppet.certname this Source no longer
// reports. A single node that fails to normalize is skipped, not fatal (§1.8).
func (s *Syncer) Sync(ctx context.Context) error {
	started := time.Now()
	prov := s.provenance()
	projector := s.store.NormalizerProjector()
	seen := []string{}
	projected := 0

	offset := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entries, err := s.fetchInventory(ctx, offset)
		if err != nil {
			return err
		}
		for _, e := range entries {
			up, err := normalizeNode(e)
			if err != nil {
				s.log.Warn("skipping node", "certname", e.Certname, "error", err)
				continue
			}
			if _, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up}); err != nil {
				if errors.Is(err, graph.ErrIdentityConflict) {
					s.log.Error("identity conflict; not merging (§1.2)", "certname", e.Certname, "error", err)
					continue
				}
				return err
			}
			projected++
			seen = append(seen, e.Certname)
		}
		offset += len(entries)
		if len(entries) < s.pageLimit {
			break // short page = last page
		}
	}

	removed, err := projector.TombstoneAbsent(ctx, prov, "puppet.certname", seen)
	if err != nil {
		return err
	}
	s.log.Info("puppet sync complete", "nodes", projected, "removed", removed, "took", time.Since(started).String())
	return nil
}

// fetchInventory GETs one page of /pdb/query/v4/inventory ordered by certname
// for stable paging (PuppetDB does not guarantee order without order_by).
func (s *Syncer) fetchInventory(ctx context.Context, offset int) ([]inventoryEntry, error) {
	q := url.Values{}
	q.Set("order_by", `[{"field":"certname"}]`)
	q.Set("limit", strconv.Itoa(s.pageLimit))
	q.Set("offset", strconv.Itoa(offset))
	if offset == 0 {
		q.Set("include_total", "true")
	}
	endpoint := s.cfg.BaseURL + "/pdb/query/v4/inventory?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("puppet: inventory request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("puppet: inventory request: %s", res.Status)
	}
	var entries []inventoryEntry
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("puppet: decode inventory: %w", err)
	}
	return entries, nil
}
