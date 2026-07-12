package awsec2

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Syncer is this Connector's projection capability (§2.2). EC2 has no delta
// feed, so the cycle is a paginated full enumeration + tombstone-absent —
// the cursor stays empty by design (ADR-0014).
type Syncer struct {
	cfg      Config
	store    *graph.Store
	log      *slog.Logger
	source   types.Source
	interval time.Duration
	client   *ec2.Client
}

// NewSyncer prepares a Syncer for one registered Source.
func NewSyncer(cfg Config, interval time.Duration, store *graph.Store, log *slog.Logger) *Syncer {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Syncer{
		cfg:      cfg,
		store:    store,
		log:      log.With("connector", "awsec2", "source", cfg.SourceName),
		interval: interval,
	}
}

// Register records the Source and claims this Syncer's Facet namespaces in
// the ownership registry (§2.1) — registration precedes any write.
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "awsec2",
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

// Run polls full enumerations until ctx ends.
func (s *Syncer) Run(ctx context.Context) error {
	client, err := s.cfg.client(ctx)
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

// Sync enumerates every instance and projects it; terminated instances (EC2
// keeps them visible for a while) count as absent, so they tombstone the
// same cycle the API stops calling them alive.
func (s *Syncer) Sync(ctx context.Context) error {
	started := time.Now()
	prov := s.provenance()
	projector := s.store.NormalizerProjector()
	seen := []string{}
	projected := 0

	pager := ec2.NewDescribeInstancesPaginator(s.client, &ec2.DescribeInstancesInput{})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, r := range page.Reservations {
			for _, in := range r.Instances {
				if in.State != nil && in.State.Name == ec2types.InstanceStateNameTerminated {
					continue
				}
				up, err := normalizeInstance(s.cfg.Region, in)
				if err != nil {
					s.log.Warn("skipping instance", "error", err)
					continue
				}
				if _, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up}); err != nil {
					if errors.Is(err, graph.ErrIdentityConflict) {
						s.log.Error("identity conflict; not merging (§1.2)", "instance", up.IdentityKeys["aws.instanceId"], "error", err)
						continue
					}
					return err
				}
				projected++
				seen = append(seen, up.IdentityKeys["aws.instanceId"])
			}
		}
	}

	if _, err := projector.TombstoneAbsent(ctx, prov, "aws.instanceId", seen); err != nil {
		return err
	}
	if err := s.store.SetSyncCursor(ctx, s.source.ID, "", true); err != nil {
		return err
	}
	s.log.Info("full sync complete", "instances", projected, "took", time.Since(started).String())
	return nil
}
