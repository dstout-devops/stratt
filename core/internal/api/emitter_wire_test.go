package api

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// ── ADR-0163 · the declaration has to survive the doors it arrives and leaves through ──────────
//
// THIS TEST EXISTS BECAUSE BOTH DOORS SILENTLY DROPPED THE FIELD when it was added. The apply path
// would have accepted an Emitter declaring a fan-out and stored one that fans nothing out; the list
// path would have shown an estate a declaration it does not have. Neither fails anywhere a test was
// looking — the symptom is a POST behaving wrongly, later, somewhere else.

const wireTokenHash = "43617af80f821a4070d15fe100b0205e16d45eff680b6a6cb9d92b81918fcf91"

func TestTheApplyDoorCarriesTheFanOutDeclaration(t *testing.T) {
	as := "batch"
	wire := DesiredState{Emitters: &[]Emitter{{
		Name: "nms", Kind: "webhook", TokenHash: wireTokenHash,
		Explode: &EmitterExplode{Path: "data.events", Merge: &[]struct {
			As   *string `json:"as,omitempty"`
			Path string  `json:"path"`
		}{{Path: "meta.site"}, {Path: "data.batchId", As: &as}}},
	}}}

	decls, err := declarationsFromWire(wire, nil)
	if err != nil {
		t.Fatalf("declarationsFromWire: %v", err)
	}
	if len(decls.Emitters) != 1 {
		t.Fatalf("got %d emitters", len(decls.Emitters))
	}
	got := decls.Emitters[0]
	if got.Explode == nil {
		t.Fatal("the fan-out declaration did not survive the apply door — the estate would be " +
			"accepted and stored as an Emitter that fans nothing out")
	}
	if got.Explode.Path != "data.events" {
		t.Errorf("path = %q", got.Explode.Path)
	}
	if len(got.Explode.Merge) != 2 || got.Explode.Merge[0].Path != "meta.site" ||
		got.Explode.Merge[1].Key() != "batch" {
		t.Errorf("merge did not survive intact: %+v", got.Explode.Merge)
	}
}

// The retired spelling means the same thing however it arrives. A normalization applied at one
// door is a rule that holds depending on the transport (ADR-0163 D2).
func TestTheApplyDoorNormalizesTheRetiredKind(t *testing.T) {
	wire := DesiredState{Emitters: &[]Emitter{{
		Name: "alerts", Kind: "alertmanager", TokenHash: wireTokenHash,
	}}}
	decls, err := declarationsFromWire(wire, nil)
	if err != nil {
		t.Fatalf("an estate applied over HTTP with the retired kind must still be accepted: %v", err)
	}
	got := decls.Emitters[0]
	if got.Kind != types.EmitterWebhook || got.Explode == nil || got.Explode.Path != "alerts" {
		t.Fatalf("the retired kind must normalize at this door too: %+v", got)
	}
}

// And the way back out.
func TestTheListDoorShowsTheFanOutDeclaration(t *testing.T) {
	out := explodeToWire(&types.ExplodeSpec{
		Path: "alerts", Merge: []types.MergeKey{{Path: "status", As: "groupStatus"}},
	})
	if out == nil || out.Path != "alerts" {
		t.Fatalf("explodeToWire: %+v", out)
	}
	if out.Merge == nil || len(*out.Merge) != 1 {
		t.Fatal("the merge list must be visible to an operator reading the estate back")
	}
	m := (*out.Merge)[0]
	if m.Path != "status" || m.As == nil || *m.As != "groupStatus" {
		t.Errorf("the rename must survive: %+v", m)
	}
	if explodeToWire(nil) != nil {
		t.Error("an Emitter with no declaration must not grow one on the way out")
	}
}
