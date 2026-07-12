package msgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// errResync marks an expired delta token (HTTP 410): the stored cursor is
// worthless and the next cycle must run a clean full enumeration.
var errResync = errors.New("msgraph: delta token expired; full resync required")

// Syncer is this Connector's projection capability (§2.2): Graph delta
// queries flowing through the Normalizer into the graph with Provenance.
type Syncer struct {
	cfg    Config
	store  *graph.Store
	log    *slog.Logger
	source types.Source
	// Interval between delta polls (Graph delta is poll-based, not a held
	// connection like vSphere's PropertyCollector).
	interval time.Duration
	client   *http.Client
}

// NewSyncer prepares a Syncer for one registered Source.
func NewSyncer(cfg Config, interval time.Duration, store *graph.Store, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Syncer{
		cfg:      cfg,
		store:    store,
		log:      log.With("connector", "msgraph", "source", cfg.SourceName),
		interval: interval,
	}
}

// Register records the Source and claims this Syncer's Facet namespaces in
// the ownership registry (§2.1) — registration precedes any write.
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "msgraph",
		Name:     s.cfg.SourceName,
		Endpoint: s.cfg.Endpoint,
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

// Run polls the delta feed until ctx ends. The stored deltaLink cursor makes
// restarts resume incrementally; an expired token degrades to one clean full
// enumeration, never silent data loss.
func (s *Syncer) Run(ctx context.Context) error {
	s.client = s.cfg.httpClient(ctx)
	for {
		if err := s.Sync(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, errResync) {
				s.log.Warn("delta token expired; clearing cursor for full resync")
				if err := s.store.SetSyncCursor(ctx, s.source.ID, "", true); err != nil {
					return err
				}
			} else {
				s.log.Error("sync cycle failed; retrying next interval", "error", err)
			}
		}
		select {
		case <-time.After(s.interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Sync runs one delta cycle: resume from the stored deltaLink when present,
// else a full enumeration (which also tombstones everything this Source no
// longer reports). The new deltaLink persists as the cursor either way.
func (s *Syncer) Sync(ctx context.Context) error {
	started := time.Now()
	cursor, err := s.store.SyncCursor(ctx, s.source.ID)
	if err != nil {
		return err
	}
	initial := cursor == ""
	url := cursor
	if initial {
		url = s.cfg.Endpoint + "/devices/delta"
	}

	prov := s.provenance()
	projector := s.store.NormalizerProjector()
	seen := []string{}
	projected, removed := 0, 0

	for url != "" {
		page, err := s.getPage(ctx, url)
		if err != nil {
			return err
		}
		for _, d := range page.Value {
			if d.Removed != nil {
				if _, err := projector.TombstoneByIdentity(ctx, prov, "graph.id", d.ID); err != nil {
					return err
				}
				removed++
				continue
			}
			up, err := normalizeDevice(d)
			if err != nil {
				s.log.Warn("skipping device", "error", err)
				continue
			}
			if _, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up}); err != nil {
				if errors.Is(err, graph.ErrIdentityConflict) {
					s.log.Error("identity conflict; not merging (§1.2)", "device", d.DisplayName, "error", err)
					continue
				}
				return err
			}
			projected++
			seen = append(seen, d.ID)
		}
		switch {
		case page.NextLink != "":
			url = page.NextLink
		case page.DeltaLink != "":
			if err := s.store.SetSyncCursor(ctx, s.source.ID, page.DeltaLink, initial); err != nil {
				return err
			}
			url = ""
		default:
			return fmt.Errorf("msgraph: delta page carried neither nextLink nor deltaLink")
		}
	}

	if initial {
		// Full enumeration: everything absent from this Source is gone.
		if _, err := projector.TombstoneAbsent(ctx, prov, "graph.id", seen); err != nil {
			return err
		}
		s.log.Info("full sync complete", "devices", projected, "took", time.Since(started).String())
	} else if projected > 0 || removed > 0 {
		s.log.Info("delta projected", "changed", projected, "removed", removed)
	}
	return nil
}

func (s *Syncer) getPage(ctx context.Context, url string) (deltaPage, error) {
	var page deltaPage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return page, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return page, fmt.Errorf("msgraph: delta request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusGone {
		return page, errResync
	}
	if res.StatusCode != http.StatusOK {
		return page, fmt.Errorf("msgraph: delta request: %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		return page, fmt.Errorf("msgraph: decode delta page: %w", err)
	}
	return page, nil
}
