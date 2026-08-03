package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

const tokenHash = "43617af80f821a4070d15fe100b0205e16d45eff680b6a6cb9d92b81918fcf91"

func webhookEmitter(mut func(*types.Emitter)) types.Emitter {
	e := types.Emitter{Name: "alerts", Kind: types.EmitterWebhook, TokenHash: tokenHash}
	mut(&e)
	return e
}

// The retired spelling still parses, and it means EXACTLY the declaration that replaced it
// (ADR-0163 D2). An estate that has said `kind: alertmanager` since ADR-0018 keeps working.
func TestTheRetiredKindNormalizesIntoTheDeclaration(t *testing.T) {
	e := types.Emitter{Name: "alerts", Kind: types.EmitterAlertmanager, TokenHash: tokenHash}
	NormalizeEmitter(&e)

	if e.Kind != types.EmitterWebhook {
		t.Fatalf("kind = %q, want webhook — the vendor's name is not a kind any more", e.Kind)
	}
	if e.Explode == nil || e.Explode.Path != "alerts" {
		t.Fatalf("explode = %+v, want the alerts[] fan-out", e.Explode)
	}
	got := []string{}
	for _, m := range e.Explode.Merge {
		got = append(got, m.Key())
	}
	if strings.Join(got, ",") != "receiver,groupLabels" {
		t.Fatalf("merge = %v, want exactly what the deleted Go struct folded in", got)
	}
	if err := ValidateEmitter(e); err != nil {
		t.Fatalf("the normalized form must validate: %v", err)
	}
}

// An explicit declaration is never overwritten by the normalization — otherwise an estate could
// not migrate off the kind while keeping its own shape.
func TestNormalizationDoesNotOverrideADeclaration(t *testing.T) {
	e := types.Emitter{Name: "alerts", Kind: types.EmitterAlertmanager, TokenHash: tokenHash,
		Explode: &types.ExplodeSpec{Path: "data.items"}}
	NormalizeEmitter(&e)
	if e.Explode.Path != "data.items" {
		t.Fatalf("the declaration wins: %+v", e.Explode)
	}
}

// A declaration that could never fan out fails ITS FILE, not every POST at 3am.
func TestExplodeRequiresAPath(t *testing.T) {
	err := ValidateEmitter(webhookEmitter(func(e *types.Emitter) {
		e.Explode = &types.ExplodeSpec{Merge: []types.MergeKey{{Path: "receiver"}}}
	}))
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("explode with no path must be refused: %v", err)
	}
	err = ValidateEmitter(webhookEmitter(func(e *types.Emitter) {
		e.Explode = &types.ExplodeSpec{Path: "alerts", Merge: []types.MergeKey{{As: "x"}}}
	}))
	if err == nil || !strings.Contains(err.Error(), "merge[0]") {
		t.Fatalf("a merge entry with no path must be refused and NAME its index: %v", err)
	}
}

// Two merged fields landing on one key is visible before any POST arrives, so it is refused
// here rather than at ingest. No precedence is invented either way (§2.4).
func TestTwoMergedFieldsCannotLandOnOneKey(t *testing.T) {
	err := ValidateEmitter(webhookEmitter(func(e *types.Emitter) {
		e.Explode = &types.ExplodeSpec{Path: "alerts", Merge: []types.MergeKey{
			{Path: "meta.site"}, {Path: "labels.site"},
		}}
	}))
	if err == nil {
		t.Fatal("two paths whose last segment is `site` must be refused")
	}
	for _, want := range []string{"site", "as:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the key and the way out; missing %q: %v", want, err)
		}
	}
	// …and `as:` resolves it at declaration time too.
	if err := ValidateEmitter(webhookEmitter(func(e *types.Emitter) {
		e.Explode = &types.ExplodeSpec{Path: "alerts", Merge: []types.MergeKey{
			{Path: "meta.site"}, {Path: "labels.site", As: "labelSite"},
		}}
	})); err != nil {
		t.Errorf("`as:` must resolve it: %v", err)
	}
}

// Nothing POSTs to a stream Emitter, so a fan-out declared on one is asking for something that
// cannot happen. Refused where it is written rather than ignored at runtime (§1.8).
func TestAStreamEmitterCannotDeclareAFanOut(t *testing.T) {
	err := ValidateEmitter(types.Emitter{Name: "salt", Kind: types.EmitterStream,
		Explode: &types.ExplodeSpec{Path: "data"}})
	if err == nil || !strings.Contains(err.Error(), "no POST") {
		t.Fatalf("a stream emitter declaring explode must be refused: %v", err)
	}
}

// The kind is gone from the closed set core will accept.
func TestTheVendorNameIsNoLongerAKind(t *testing.T) {
	err := ValidateEmitter(types.Emitter{Name: "alerts", Kind: "alertmanager", TokenHash: tokenHash})
	if err == nil {
		t.Fatal("an un-normalized `alertmanager` kind must not validate — it is a spelling the " +
			"parser rewrites, not a kind core still knows")
	}
	// The message quotes back what was declared — that is good diagnosis, not advertising. What
	// must not survive is the vendor's name inside the list of kinds core OFFERS.
	if !strings.Contains(err.Error(), "(webhook, stream)") {
		t.Errorf("the offered set must be the two real kinds: %v", err)
	}
}
