package mockstratt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The governor. This is the part that must not be approximated: it is a faithful
// implementation of core's Apply governance (core/internal/pluginhost.govern), and
// core/internal/pluginhost's parity test drives both over the same table and
// asserts identical verdicts. If the two ever disagree, THIS is the bug — a plugin
// that passes a lenient mock and fails the real host is the specific failure this
// package exists to prevent.

// ApplyTarget is one core-resolved target as it crosses the port. The core
// resolves the set; the plugin renders it into tool-native content and keys its
// results back by Name. Nothing here is negotiable by the plugin — that asymmetry
// IS the confused-deputy gate (ADR-0047 §1.1/§1.2/§1.8).
type ApplyTarget struct {
	Name         string
	Address      string
	Port         int32
	Vars         map[string]string
	IdentityKeys map[string]string
	Jump         []JumpHop
}

// JumpHop is one resolved link in a reached-via chain, nearest hop first
// (ADR-0126 D3). Coordinates only, never a credential.
type JumpHop struct {
	Name    string
	Address string
	Port    int32
}

// Entity is a governed write-back proposal that SURVIVED the gates. Note what it
// does not carry: no principal, no provenance, no graph id. A plugin proposes; the
// core stamps and writes (§1.2). Seeing the stamped-free shape is the lesson.
type Entity struct {
	Kind         string
	IdentityKeys map[string]string
	Labels       map[string]string
	Facets       map[string][]byte
}

// DerivedSchema is a tool-derived (rung-2) or declared (rung-3) schema document
// the plugin emitted about its OWN outputs. Rung-1 is sovereign and deliberately
// not representable over the wire (ADR-0046 finding #2).
type DerivedSchema struct {
	SchemaID string
	Rev      string
	Schema   []byte
	Rung     int32
}

// Rejection is a governance refusal — something the plugin emitted and the host
// DROPPED. These are the most useful output of this package: in production they
// become Findings the plugin author never sees, so surfacing them at development
// time is most of the value on offer (§1.8 — the abstraction must never hide
// diagnosis, and "silently discarded" is the purest form of hiding it).
type Rejection struct {
	Kind   string // item-result | identity-scheme | entity | label | facet | derived-contract
	Detail string // the offending value
	Reason string
}

func (r Rejection) String() string { return r.Kind + " " + r.Detail + ": " + r.Reason }

// Event is one typed diagnostic frame the plugin emitted (TaskEvent, invariant
// #12). It is deliberately NOT an opaque blob: §1.8 one-click descent must survive
// the port, so diagnostics ride a typed channel while only desired-state payloads
// are opaque.
//
// These are RETAINED rather than folded away because the core persists every one
// of them as a Run event, and they are usually where the actual cause of a failure
// is written — the terminal often carries only a summary ("ansible-runner rc=4")
// while the reason ("ssh: no route to host") arrived on an earlier frame. A
// harness that kept only the terminal would hide the diagnosis it exists to
// surface.
type Event struct {
	Level   string
	Message string
	Fields  map[string]string
	Scope   string
	// Terminal marks the final frame of the stream; Ok is meaningful only then.
	Terminal bool
	Ok       bool
}

// Result is the governed, UNPROJECTED outcome of one Apply — what the core would
// hold in hand at the moment before it writes anything.
type Result struct {
	Succeeded bool
	// Error is the plugin's OWN account of why it failed: the message on a red
	// terminal. Kept because a failed Run whose cause lives only in a deleted pod
	// log is a §1.8 dead end (ADR-0117 D5c).
	Error      string
	PerTarget  map[string]string // resolved target name -> status; sticky-fail folded
	WriteBack  []Entity
	Drift      map[string][]json.RawMessage
	Derived    []DerivedSchema
	Checkpoint string // graceful-abort resume token (invariant #7); "" == ran to completion
	Rejections []Rejection
	// SawTerminal records whether the stream ever terminated. It is kept SEPARATE
	// from Succeeded because the two failures need different fixes and Succeeded
	// alone cannot tell them apart: "the plugin ran and a host failed" is a
	// day-to-day outcome, while "the plugin died mid-stream" is a defect in the
	// plugin. Collapsing them sends the reader to the wrong place (§1.8).
	SawTerminal bool
	// Events is every typed TaskEvent the plugin emitted, in order — the descent
	// stream the core persists against the Run.
	Events []Event
	// Diagnostics is everything the plugin wrote that was NOT a port frame — the
	// pod-log half of the stream (banners, tracebacks, a panic). The real
	// dispatcher routes these to a diagnostic ring; they are retained here for the
	// same reason, because a plugin that dies before its first frame has nothing
	// else to say for itself.
	Diagnostics []string
}

// Log renders the typed event stream plus the untyped diagnostics as lines — the
// descent view of a Run, and the right thing to put in a test failure message.
func (r Result) Log() string {
	var b strings.Builder
	for _, e := range r.Events {
		b.WriteString(e.Level)
		if e.Scope != "" && e.Scope != "UNSPECIFIED" {
			b.WriteString(" [" + e.Scope + "]")
		}
		b.WriteString(" " + e.Message + "\n")
	}
	for _, d := range r.Diagnostics {
		b.WriteString("(untyped) " + d + "\n")
	}
	return b.String()
}

// Failed reports whether the run did not succeed. Provided because `!Succeeded` at
// a call site reads as "not succeeded" when the meaning is stronger: a torn stream
// that never terminated is a FAILURE, not an absence of success.
func (r Result) Failed() bool { return !r.Succeeded }

// Host is the plugin-facing core: one grant, one governor. It holds no store, no
// broker and no scheduler, because a plugin can observe none of those.
type Host struct {
	grant Grant
	// writeScope is the per-Run facet FLOOR (ADR-0054). The effective allowlist is
	// grant ∩ writeScope — a pure AND, never a fallback (§2.4). TIGHT default: a
	// nil scope admits NO facet write-back, which is least authority and also the
	// single most common reason a plugin author's facets "disappear".
	writeScope map[string]bool
}

// NewHost builds a host governing under grant. Use WithFacetWriteScope to open the
// per-Run floor; with none, every facet write-back is refused — deliberately, and
// with a Rejection that says so.
func NewHost(grant Grant) *Host {
	return &Host{grant: grant, writeScope: map[string]bool{}}
}

// WithFacetWriteScope sets the per-Run facet write-scope floor a Step would carry
// (ADR-0054). Returns the host for chaining.
func (h *Host) WithFacetWriteScope(ns ...string) *Host {
	h.writeScope = make(map[string]bool, len(ns))
	for _, n := range ns {
		h.writeScope[n] = true
	}
	return h
}

// Grant returns the grant this host governs under.
func (h *Host) Grant() Grant { return h.grant }

// Stream is the transport-agnostic source of ApplyResponses the governor consumes
// (ADR-0051): the gRPC client stream satisfies it, and so does the subprocess
// stdout adapter. io.EOF ends the stream.
type Stream interface {
	Recv() (*pluginv1.ApplyResponse, error)
}

// Govern is the SOLE judge of an Apply stream, whichever transport produced it.
//
// The rules, in the order a reader will want them:
//
//   - CONFUSED-DEPUTY GATE. A per-target result keyed to a name outside the
//     core-resolved set is refused. The core holds the target set; the plugin's
//     self-reported inventory is never allowed to widen it.
//   - ASYMMETRIC TERMINAL TRUST (§1.8). A RED terminal is believed — a plugin
//     declaring its own failure is the most reliable signal there is. A GREEN
//     terminal is NOT: the per-target results must agree, so a plugin cannot
//     declare a success it did not achieve.
//   - NO TERMINAL IS A FAILURE. A stream that just stops is torn, not converged.
//   - FACETS = GRANT ∩ WRITE-SCOPE. Outside either bound, it drops.
//   - IDENTITY = TIER AND GRANT. An entity left with no granted identity key is
//     dropped whole — it could not be correlated to anything.
//
// Every refusal is recorded, never silent.
func (h *Host) Govern(ctx context.Context, stream Stream, targets []ApplyTarget) (Result, error) {
	resolved := make(map[string]bool, len(targets))
	for _, t := range targets {
		resolved[t.Name] = true
	}

	out := Result{PerTarget: map[string]string{}}
	var failed, sawTerminal bool
	reject := func(kind, detail, reason string) {
		out.Rejections = append(out.Rejections, Rejection{Kind: kind, Detail: detail, Reason: reason})
	}

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("mockstratt: apply recv: %w", err)
		}

		if ev := resp.GetEvent(); ev != nil {
			out.Events = append(out.Events, Event{
				Level:    strings.TrimPrefix(ev.GetLevel().String(), "LEVEL_"),
				Message:  ev.GetMessage(),
				Fields:   ev.GetFields(),
				Scope:    strings.TrimPrefix(ev.GetScope().String(), "SCOPE_"),
				Terminal: ev.GetTerminal(),
				Ok:       ev.GetOk(),
			})
			if cp := ev.GetCheckpoint(); cp != "" {
				out.Checkpoint = cp
			}
			if ev.GetTerminal() {
				sawTerminal = true
				if !ev.GetOk() {
					failed = true
					out.Error = ev.GetMessage() // and WHY, not just that it did
				}
			}
		}

		// Per-target status: confused-deputy gated, sticky-fail folded.
		if r := resp.GetResult(); r != nil {
			key := r.GetItemKey()
			switch {
			case key != "" && !resolved[key]:
				reject("item-result", key,
					"apply: per-target status for a target outside the resolved set (confused deputy)")
			default:
				st := applyStatus(r.GetStatus())
				if st == "failed" || st == "unreachable" {
					failed = true
				}
				if key != "" {
					if prev := out.PerTarget[key]; prev != "failed" && prev != "unreachable" {
						out.PerTarget[key] = st // sticky: a failed target is never downgraded
					}
				}
			}
		}

		// Write-back: tier+grant identity gate, label gate, grant ∩ scope facet gate.
		for _, e := range resp.GetWriteBack() {
			if ent, ok := h.governEntity(e, stepScoped, "apply", reject); ok {
				out.WriteBack = append(out.WriteBack, ent)
			}
		}

		// Drift: opaque, already-redacted, accumulated per item_key (ADR-0019).
		if d := resp.GetDrift(); d != nil {
			if out.Drift == nil {
				out.Drift = map[string][]json.RawMessage{}
			}
			out.Drift[d.GetItemKey()] = append(out.Drift[d.GetItemKey()], json.RawMessage(d.GetDetail().GetBytes()))
		}

		// Derived contract: namespace-confined to the plugin's own Source scope.
		if dc := resp.GetDerivedContract(); dc != nil {
			id := dc.GetSchemaId()
			if !strings.HasPrefix(id, h.grant.SourceName+"/") {
				reject("derived-contract", id,
					"apply: schema_id outside the plugin's Source namespace (ADR-0047 §4)")
			} else {
				out.Derived = append(out.Derived, DerivedSchema{
					SchemaID: id, Rev: dc.GetRev(), Schema: dc.GetSchema(), Rung: int32(dc.GetRung()),
				})
			}
		}
	}

	// The core-side fold. Both halves matter: a green terminal the per-target
	// results contradict is not success, and no terminal at all is not success
	// either.
	out.SawTerminal = sawTerminal
	out.Succeeded = sawTerminal && !failed
	return out, nil
}

// facetBound says WHICH facet ceiling an entity is governed under. The two paths
// genuinely differ, and the difference is passed explicitly rather than inferred
// from an empty scope map, because on the Apply path an empty floor means DENY ALL
// while on the Observe path there is no floor at all — the same value carrying
// opposite meanings is the implicit precedence §2.4 forbids, and it would be a
// deny-by-default rule quietly turning into an allow.
type facetBound int

const (
	// stepScoped is the Apply path: facets = grant ∩ the Step's write-scope
	// (ADR-0054), a pure AND.
	stepScoped facetBound = iota
	// grantOnly is the Observe path: a Syncer's ownership is registered from the
	// grant and there is no Step, so the grant is the only bound.
	grantOnly
)

// governEntity applies the identity, label and facet gates to one proposed entity.
// Apply write-back and Observe share this body deliberately — the two paths
// diverging is a defect, not a feature, and the ONE thing they legitimately differ
// on is named in the bound parameter above.
func (h *Host) governEntity(e *pluginv1.ObservedEntity, bound facetBound, verb string, reject func(kind, detail, reason string)) (Entity, bool) {
	ids := map[string]string{}
	for scheme, val := range e.GetIdentityKeys() {
		if ok, reason := h.grant.allowsIdentity(scheme); !ok {
			reject("identity-scheme", scheme, verb+": "+reason)
			continue
		}
		ids[scheme] = val
	}
	// An entity with no granted identity key is dropped WHOLE, not written with
	// what survived: it could not be correlated to anything, so writing it would
	// create an orphan the core can never reconcile or tombstone.
	if len(ids) == 0 {
		reject("entity", e.GetKind(), verb+": no granted identity key")
		return Entity{}, false
	}
	labels := map[string]string{}
	for k, v := range e.GetLabels() {
		if !h.grant.allowsLabel(k) {
			reject("label", k, verb+": label key not in operator grant")
			continue
		}
		labels[k] = v
	}
	facets := map[string][]byte{}
	for ns, v := range e.GetFacets() {
		if !h.grant.allowsFacet(ns) {
			reject("facet", ns, verb+": facet namespace not in operator grant")
			continue
		}
		if bound == stepScoped && !h.writeScope[ns] {
			reject("facet", ns,
				verb+": facet namespace not in the Step's facet write-scope (least authority, ADR-0054)")
			continue
		}
		facets[ns] = v
	}
	return Entity{Kind: e.GetKind(), IdentityKeys: ids, Labels: labels, Facets: facets}, true
}

// applyStatus renders a wire ItemResult.Status as the core-legible per-target
// string.
func applyStatus(s pluginv1.ItemResult_Status) string {
	switch s {
	case pluginv1.ItemResult_STATUS_OK:
		return "ok"
	case pluginv1.ItemResult_STATUS_CHANGED:
		return "changed"
	case pluginv1.ItemResult_STATUS_FAILED:
		return "failed"
	case pluginv1.ItemResult_STATUS_UNREACHABLE:
		return "unreachable"
	default:
		return "unspecified"
	}
}
