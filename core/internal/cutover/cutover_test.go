package cutover

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// fakeManifest is a ManifestSource returning one fixed cutover descriptor — the reconciler
// learns the relation/facet to check ONLY from here (tool-blindness under test).
type fakeManifest struct{ desc *pluginv1.CutoverDescriptor }

func (f fakeManifest) GetManifest(context.Context, *pluginv1.GetManifestRequest, ...grpc.CallOption) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{Cutover: []*pluginv1.CutoverDescriptor{f.desc}}}, nil
}

type fakeStore struct {
	workflows []types.Workflow
	entity    map[string]string
	sources   map[string][]string
	facets    map[string][]types.Facet
	opened    []string
	resolved  [][2]string
}

func (s *fakeStore) ListWorkflows(context.Context) ([]types.Workflow, error) { return s.workflows, nil }
func (s *fakeStore) EntityIDByIdentity(_ context.Context, scheme, value string) (string, bool, error) {
	id, ok := s.entity[scheme+"\x00"+value]
	return id, ok, nil
}
func (s *fakeStore) RelationSources(_ context.Context, toID, rel string) ([]string, error) {
	return s.sources[toID+"\x00"+rel], nil
}
func (s *fakeStore) GetFacets(_ context.Context, id string) ([]types.Facet, error) {
	return s.facets[id], nil
}
func (s *fakeStore) WriteGovernanceFinding(_ context.Context, _, target, _, _ string, _ []byte) error {
	s.opened = append(s.opened, target)
	return nil
}
func (s *fakeStore) ResolveClearedFindingsByFramework(_ context.Context, _ string, keep [][2]string) (int64, error) {
	s.resolved = keep
	return 0, nil
}

func newStore(enabled bool) *fakeStore {
	facet, _ := json.Marshal(map[string]any{"name": "Nightly", "enabled": enabled})
	return &fakeStore{
		workflows: []types.Workflow{{
			Name:        "deploy-web",
			AdoptedFrom: &types.AdoptedFrom{Kind: "ansible.template", Identity: "ctrl-a/10", Source: "ctrl-a"},
		}},
		entity:  map[string]string{"ansible.template\x00ctrl-a/10": "tid-10"},
		sources: map[string][]string{"tid-10\x00schedules": {"sched-30"}},
		facets:  map[string][]types.Facet{"sched-30": {{Namespace: "ansible.schedule", Value: facet}}},
	}
}

func reconciler(store Store) *Reconciler {
	return &Reconciler{Store: store, Clients: []ManifestSource{fakeManifest{desc: &pluginv1.CutoverDescriptor{
		TargetKind: "ansible.template", Relation: "schedules",
		LivenessNamespace: "ansible.schedule", LivenessPath: "enabled", LivenessValue: "true",
	}}}}
}

// TestSweepOpensFindingForLiveSchedule: an adopted template whose AWX schedule is still
// enabled is a double-execution → a Finding on the schedule to disable.
func TestSweepOpensFindingForLiveSchedule(t *testing.T) {
	st := newStore(true)
	if err := reconciler(st).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(st.opened) != 1 || st.opened[0] != "sched-30" {
		t.Fatalf("expected a Finding on sched-30, got %v", st.opened)
	}
	if len(st.resolved) != 1 || st.resolved[0] != [2]string{"adopt-cutover", "sched-30"} {
		t.Fatalf("keep set should retain the live finding, got %v", st.resolved)
	}
}

// TestSweepNoFindingWhenScheduleDisabled: once the schedule is disabled the cutover is
// complete — no Finding, and the keep set is empty so any prior Finding resolves.
func TestSweepNoFindingWhenScheduleDisabled(t *testing.T) {
	st := newStore(false)
	if err := reconciler(st).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(st.opened) != 0 {
		t.Fatalf("no Finding expected for a disabled schedule, got %v", st.opened)
	}
	if len(st.resolved) != 0 {
		t.Fatalf("empty keep set expected (cleared findings resolve), got %v", st.resolved)
	}
}

// TestSweepIgnoresNonAdopted: a hand-written Workflow (no adoptedFrom) is never joined.
func TestSweepIgnoresNonAdopted(t *testing.T) {
	st := newStore(true)
	st.workflows = append(st.workflows, types.Workflow{Name: "handwritten"})
	if err := reconciler(st).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(st.opened) != 1 {
		t.Fatalf("only the adopted workflow's live schedule should fire, got %v", st.opened)
	}
}

// TestSweepNoDescriptorNoop: with no Connector declaring a descriptor, the sweep is a no-op
// (never touches the store) — the spine stays tool-blind, learning nothing on its own.
func TestSweepNoDescriptorNoop(t *testing.T) {
	st := newStore(true)
	rc := &Reconciler{Store: st} // no Clients ⇒ no descriptors
	if err := rc.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(st.opened) != 0 || st.resolved != nil {
		t.Fatalf("no-descriptor sweep must be a no-op, opened=%v resolved=%v", st.opened, st.resolved)
	}
}
