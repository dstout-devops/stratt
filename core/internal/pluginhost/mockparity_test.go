package pluginhost

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dstout-devops/stratt/sdk/mockstratt"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// PARITY: sdk/mockstratt governs exactly as this package does.
//
// mock-stratt (ADR-0137 D6) reimplements Apply governance so a plugin can be
// developed against a real ceiling without a control plane. Reimplementing it
// creates the obvious hazard — two copies of a governance rule, drifting — and
// this is the mechanism that catches it. Both governors are fed the SAME frames
// and must reach the SAME verdict.
//
// THE DEPENDENCY DIRECTION IS WHY THE TEST LIVES HERE. core may import sdk; sdk
// must never import core, or a plugin testing itself would pull in the control
// plane and disprove the very claim the harness makes. So the comparison can only
// be written on this side.
//
// WHEN THIS FAILS, THE MOCK IS USUALLY WRONG. A plugin that passes a lenient mock
// and is dropped on the floor in production is the specific failure mode
// mock-stratt exists to prevent. Fix sdk/mockstratt to match — and change this
// package only when the RULE itself is meant to change, in which case both sides
// move together and deliberately.

func parityGrant() (Grant, mockstratt.Grant) {
	core := Grant{
		PluginIdentity:  "fake",
		Tier:            TierCommunity,
		Source:          types.Source{Name: "fake-source"},
		FacetNamespaces: []string{"os.kernel", "app.config"},
		LabelKeys:       []string{"env"},
		IdentitySchemes: []string{"host.name", "dns.fqdn"},
	}
	mock := mockstratt.Grant{
		PluginIdentity:  core.PluginIdentity,
		Tier:            mockstratt.TierCommunity,
		SourceName:      core.Source.Name,
		FacetNamespaces: core.FacetNamespaces,
		LabelKeys:       core.LabelKeys,
		IdentitySchemes: core.IdentitySchemes,
	}
	return core, mock
}

// lineFeeder replays proto-JSON frames. Both governors consume the SAME encoded
// bytes rather than two hand-built object graphs, so an encoding-level divergence
// cannot hide behind the comparison.
type lineFeeder struct {
	lines [][]byte
	i     int
}

func (f *lineFeeder) next() ([]byte, error) {
	if f.i >= len(f.lines) {
		return nil, io.EOF
	}
	f.i++
	return f.lines[f.i-1], nil
}

func (f *lineFeeder) Recv() (*pluginv1.ApplyResponse, error) {
	line, err := f.next()
	if err != nil {
		return nil, err
	}
	resp := &pluginv1.ApplyResponse{}
	if uerr := protojson.Unmarshal(line, resp); uerr != nil {
		return nil, uerr
	}
	return resp, nil
}

func encode(t *testing.T, frames ...*pluginv1.ApplyResponse) [][]byte {
	t.Helper()
	out := make([][]byte, 0, len(frames))
	for _, f := range frames {
		b, err := protojson.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func pTerminal(ok bool, msg string) *pluginv1.ApplyResponse {
	return &pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Terminal: true, Ok: ok, Message: msg, At: timestamppb.Now(),
	}}
}

func pResult(key string, st pluginv1.ItemResult_Status) *pluginv1.ApplyResponse {
	return &pluginv1.ApplyResponse{Result: &pluginv1.ItemResult{ItemKey: key, Status: st}}
}

func pEntity(kind string, ids, labels map[string]string, facets map[string][]byte) *pluginv1.ApplyResponse {
	return &pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
		Kind: kind, IdentityKeys: ids, Labels: labels, Facets: facets,
	}}}
}

func TestMockStrattGovernsIdenticallyToCore(t *testing.T) {
	coreGrant, mockGrant := parityGrant()

	cases := []struct {
		name    string
		targets []string
		scope   []string
		frames  []*pluginv1.ApplyResponse
	}{
		{
			name: "clean success", targets: []string{"web-1", "web-2"}, scope: []string{"os.kernel"},
			frames: []*pluginv1.ApplyResponse{
				pResult("web-1", pluginv1.ItemResult_STATUS_CHANGED),
				pResult("web-2", pluginv1.ItemResult_STATUS_OK),
				pTerminal(true, "converged"),
			},
		},
		{
			name: "green terminal contradicted by a failed target", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{
				pResult("web-1", pluginv1.ItemResult_STATUS_FAILED),
				pTerminal(true, "converged"),
			},
		},
		{
			name: "red terminal is believed, with its message", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{pTerminal(false, "git clone refused")},
		},
		{
			name:   "torn stream — no terminal at all",
			frames: []*pluginv1.ApplyResponse{pResult("web-1", pluginv1.ItemResult_STATUS_OK)}, targets: []string{"web-1"},
		},
		{
			name: "confused deputy", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{
				pResult("web-1", pluginv1.ItemResult_STATUS_OK),
				pResult("db-1", pluginv1.ItemResult_STATUS_OK),
				pTerminal(true, "done"),
			},
		},
		{
			name: "sticky failure is not downgraded", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{
				pResult("web-1", pluginv1.ItemResult_STATUS_UNREACHABLE),
				pResult("web-1", pluginv1.ItemResult_STATUS_OK),
				pTerminal(true, "done"),
			},
		},
		{
			name: "facet ceiling — grant AND scope", targets: []string{"web-1"}, scope: []string{"os.kernel"},
			frames: []*pluginv1.ApplyResponse{
				pEntity("host", map[string]string{"host.name": "web-1"}, map[string]string{"env": "prod", "team": "x"},
					map[string][]byte{"os.kernel": []byte(`{"v":1}`), "app.config": []byte(`{"v":2}`), "billing": []byte(`{"v":3}`)}),
				pTerminal(true, "done"),
			},
		},
		{
			name: "no write scope admits nothing", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{
				pEntity("host", map[string]string{"host.name": "web-1"}, nil, map[string][]byte{"os.kernel": []byte(`{}`)}),
				pTerminal(true, "done"),
			},
		},
		{
			name: "shared identity scheme refused at community tier", targets: []string{"web-1"}, scope: []string{"os.kernel"},
			frames: []*pluginv1.ApplyResponse{
				pEntity("host", map[string]string{"dns.fqdn": "web-1.example.com"}, nil, map[string][]byte{"os.kernel": []byte(`{}`)}),
				pTerminal(true, "done"),
			},
		},
		{
			name: "entity with no granted identity is dropped whole", targets: []string{"web-1"}, scope: []string{"os.kernel"},
			frames: []*pluginv1.ApplyResponse{
				pEntity("host", map[string]string{"vcenter.uuid": "42"}, nil, map[string][]byte{"os.kernel": []byte(`{}`)}),
				pTerminal(true, "done"),
			},
		},
		{
			name: "derived contract namespace confinement", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{
				{DerivedContract: &pluginv1.DerivedContract{SchemaId: "fake-source/out", Rev: "1", Schema: []byte(`{}`)}},
				{DerivedContract: &pluginv1.DerivedContract{SchemaId: "elsewhere/out", Rev: "1", Schema: []byte(`{}`)}},
				pTerminal(true, "done"),
			},
		},
		{
			name: "checkpoint and drift", targets: []string{"web-1"},
			frames: []*pluginv1.ApplyResponse{
				{Event: &pluginv1.TaskEvent{Message: "aborting", Checkpoint: "batch-3", At: timestamppb.Now()}},
				{Drift: &pluginv1.DiffFragment{ItemKey: "web-1", Detail: &pluginv1.Payload{Bytes: []byte(`{"port":"9090"}`)}}},
				pTerminal(false, "aborted"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := encode(t, tc.frames...)

			coreTargets := make([]ApplyTarget, 0, len(tc.targets))
			mockTargets := make([]mockstratt.ApplyTarget, 0, len(tc.targets))
			for _, n := range tc.targets {
				coreTargets = append(coreTargets, ApplyTarget{Name: n, IdentityKeys: map[string]string{"host.name": n}})
				mockTargets = append(mockTargets, mockstratt.ApplyTarget{Name: n, IdentityKeys: map[string]string{"host.name": n}})
			}

			ch := New(nil, nil, coreGrant, slog.New(slog.DiscardHandler))
			coreRes, err := ch.GovernStream(context.Background(),
				NewJobStream((&lineFeeder{lines: lines}).next), coreTargets, tc.scope)
			if err != nil {
				t.Fatalf("core govern: %v", err)
			}

			mh := mockstratt.NewHost(mockGrant).WithFacetWriteScope(tc.scope...)
			mockRes, err := mh.Govern(context.Background(), &lineFeeder{lines: lines}, mockTargets)
			if err != nil {
				t.Fatalf("mock govern: %v", err)
			}

			// The verdict.
			if coreRes.Succeeded != mockRes.Succeeded {
				t.Errorf("Succeeded: core=%v mock=%v", coreRes.Succeeded, mockRes.Succeeded)
			}
			if coreRes.Error != mockRes.Error {
				t.Errorf("Error: core=%q mock=%q", coreRes.Error, mockRes.Error)
			}
			if coreRes.Checkpoint != mockRes.Checkpoint {
				t.Errorf("Checkpoint: core=%q mock=%q", coreRes.Checkpoint, mockRes.Checkpoint)
			}
			if !reflect.DeepEqual(coreRes.PerTarget, mockRes.PerTarget) {
				t.Errorf("PerTarget: core=%+v mock=%+v", coreRes.PerTarget, mockRes.PerTarget)
			}

			// What survived the gates. Compared field by field because the two
			// packages have their own entity types by design — the SHAPES may
			// differ, the DECISIONS may not.
			if len(coreRes.WriteBack) != len(mockRes.WriteBack) {
				t.Fatalf("WriteBack count: core=%d mock=%d", len(coreRes.WriteBack), len(mockRes.WriteBack))
			}
			for i := range coreRes.WriteBack {
				c, m := coreRes.WriteBack[i], mockRes.WriteBack[i]
				if c.Kind != m.Kind ||
					!reflect.DeepEqual(c.IdentityKeys, m.IdentityKeys) ||
					!reflect.DeepEqual(c.Labels, m.Labels) ||
					!reflect.DeepEqual(c.Facets, m.Facets) {
					t.Errorf("WriteBack[%d]:\n  core=%+v\n  mock=%+v", i, c, m)
				}
			}

			if len(coreRes.Derived) != len(mockRes.Derived) {
				t.Fatalf("Derived count: core=%d mock=%d", len(coreRes.Derived), len(mockRes.Derived))
			}
			for i := range coreRes.Derived {
				if coreRes.Derived[i].SchemaID != mockRes.Derived[i].SchemaID {
					t.Errorf("Derived[%d]: core=%q mock=%q", i, coreRes.Derived[i].SchemaID, mockRes.Derived[i].SchemaID)
				}
			}

			if !reflect.DeepEqual(coreRes.Drift, mockRes.Drift) {
				t.Errorf("Drift: core=%+v mock=%+v", coreRes.Drift, mockRes.Drift)
			}

			// And the refusals. A mock that reached the right verdict while
			// refusing different things would still teach plugin authors the
			// wrong rules — which is most of what the harness is FOR.
			if !reflect.DeepEqual(rejectionKeys(coreRes.Rejections), mockRejectionKeys(mockRes.Rejections)) {
				t.Errorf("Rejections:\n  core=%v\n  mock=%v",
					rejectionKeys(coreRes.Rejections), mockRejectionKeys(mockRes.Rejections))
			}
		})
	}
}

// rejectionKeys reduces refusals to (kind, detail) pairs, sorted. The REASON text
// is deliberately excluded: it is prose meant for a human, and pinning it here
// would turn every wording improvement into a cross-module test failure.
func rejectionKeys(rs []Rejection) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Kind+"/"+r.Detail)
	}
	sort.Strings(out)
	return out
}

func mockRejectionKeys(rs []mockstratt.Rejection) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Kind+"/"+r.Detail)
	}
	sort.Strings(out)
	return out
}
