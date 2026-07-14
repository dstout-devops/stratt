package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/types"
)

// accessRecertAction is the audit action recorded for a recertification
// attestation; "last attested" is a query over the audit stream (ADR-0036).
const accessRecertAction = "access.recertify"

// accessRecertCadenceDays is how long a View may go unattested before its
// access posture is flagged overdue (§1.8 — abandoned governance is never
// silent). A Git-policy knob later; a sane default now.
const accessRecertCadenceDays int64 = 90

// accessGrant is one observed/desired host-access tuple (matches the bare
// access.grants Facet element — per-element provenance lives on the desired
// side, §1.2). `subject` is a host-local account, not a platform Principal (§2).
type accessGrant struct {
	Subject string `json:"subject"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
}

// GetAccessRecertification implements (GET /access/recertification/{view}): the
// current host-access grants across a View joined with the desired
// Intent/Access grants, with unmanaged grants flagged and an overdue status
// (ADR-0036). Read-only over the projected access.grants Facets + the audit
// stream.
func (s *Server) GetAccessRecertification(w http.ResponseWriter, r *http.Request, view string) {
	// A grant listing (sudo scopes, key fingerprints, rogue access) is a
	// who-can-access-what disclosure — gate the read like the audit stream
	// (§1.6 one authorization model), not like the ungated compliance score.
	if !s.requireGrant(w, r, authz.RelationReader, "view:"+view) {
		return
	}
	report, err := s.buildRecertification(r, view)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// AttestAccessRecertification implements (POST /access/recertification/{view}):
// a Principal attests the View's current grants. The reviewed set seals as an
// object-locked Evidence bundle (ADR-0029) and the attestation is recorded in
// the one audit stream (ADR-0034). Requires runner on the View (§2.5); a
// grant-set-hash mismatch is 409 (attesting stale state is refused, §1.8).
func (s *Server) AttestAccessRecertification(w http.ResponseWriter, r *http.Request, view string) {
	if !s.requireGrant(w, r, authz.RelationRunner, "view:"+view) {
		return
	}
	var body AttestRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid attest request: "+err.Error())
		return
	}
	if body.GrantSetHash == "" {
		writeErr(w, http.StatusBadRequest, "grantSetHash is required (attest a specific grant set)")
		return
	}
	report, err := s.buildRecertification(r, view)
	if err != nil {
		s.fail(w, err)
		return
	}
	if body.GrantSetHash != report.GrantSetHash {
		writeErr(w, http.StatusConflict, fmt.Sprintf(
			"grant set changed since review (attested %s, current %s) — re-review before attesting",
			body.GrantSetHash, report.GrantSetHash))
		return
	}
	id, kind, _ := authz.PrincipalFrom(r.Context())

	// Seal the reviewed set as a WORM Evidence bundle. Without an object store
	// the attestation is still audited (the durable record) but not sealed —
	// mirroring Findings opening unsealed when no store is configured.
	bundle, _ := json.MarshalIndent(map[string]any{
		"schema":       "stratt.access-recert/v1",
		"view":         view,
		"attestedBy":   id,
		"grantSetHash": report.GrantSetHash,
		"grants":       report.Grants,
		"note":         body.Note,
		"attestedAt":   time.Now().UTC(),
	}, "", "  ")
	sum := sha256.Sum256(bundle)
	receipt := AttestationReceipt{
		View: view, GrantSetHash: report.GrantSetHash, AttestedBy: id,
		Sha256: hex.EncodeToString(sum[:]),
	}
	if s.Evidence != nil {
		key := fmt.Sprintf("access-recert/%s/%s.json", view, report.GrantSetHash)
		sealed, err := s.Evidence.Seal(r.Context(), key, bundle)
		if err != nil {
			s.fail(w, err)
			return
		}
		receipt.EvidenceKey = sealed.Key
		receipt.Sha256 = sealed.SHA256
		receipt.RetainUntil = sealed.RetainUntil
	}

	// Record the attestation in the one audit stream (§1.6). Detail carries no
	// secret material (§2.5) — grant tuples are access metadata, not credentials.
	obj := "view:" + view
	detail, _ := json.Marshal(map[string]any{
		"grantSetHash": report.GrantSetHash,
		"evidenceKey":  receipt.EvidenceKey,
		"sha256":       receipt.Sha256,
		"grantCount":   len(report.Grants),
		"note":         body.Note,
	})
	if err := s.Store.RecordAudit(r.Context(), types.AuditEvent{
		PrincipalID: id, PrincipalKind: kind, Action: accessRecertAction,
		Object: obj, Outcome: "success", Detail: detail,
	}); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

// buildRecertification folds a View's observed access.grants Facets and its
// desired Intent/Access grants into a per-grant review + an attestation hash +
// an overdue status. Split from the handlers so the fold is unit-testable.
func (s *Server) buildRecertification(r *http.Request, view string) (AccessRecertification, error) {
	ctx := r.Context()
	_, ents, err := s.Store.ResolveView(ctx, view, nil, 0)
	if err != nil {
		return AccessRecertification{}, err
	}
	// observed: grant tuple → hosts it appears on.
	hostsByGrant := map[accessGrant]map[string]bool{}
	for _, e := range ents {
		facets, err := s.Store.GetFacets(ctx, e.ID)
		if err != nil {
			return AccessRecertification{}, err
		}
		for _, f := range facets {
			if f.Namespace != "access.grants" {
				continue
			}
			var grants []accessGrant
			if json.Unmarshal(f.Value, &grants) != nil {
				continue
			}
			for _, g := range grants {
				if hostsByGrant[g] == nil {
					hostsByGrant[g] = map[string]bool{}
				}
				hostsByGrant[g][e.ID] = true
			}
		}
	}
	// desired: grant tuple → declaring (intent, assignment).
	type origin struct{ intent, assignment string }
	desired := map[accessGrant]origin{}
	desiredAll := []accessGrant{}
	assignments, err := s.Store.ListAssignments(ctx)
	if err != nil {
		return AccessRecertification{}, err
	}
	for _, a := range assignments {
		if a.View != view {
			continue
		}
		in, err := s.Store.GetIntent(ctx, a.Intent)
		if err != nil || in.Kind != types.IntentAccess {
			continue
		}
		g := accessGrant{
			Subject: specString(in.Spec, "subject"),
			Kind:    specString(in.Spec, "kind"),
			Scope:   specString(in.Spec, "scope"),
		}
		if g.Subject == "" || g.Kind == "" {
			continue
		}
		if _, dup := desired[g]; !dup {
			desiredAll = append(desiredAll, g)
		}
		desired[g] = origin{intent: in.Name, assignment: a.Name}
	}

	reviews := buildGrantReviews(hostsByGrant, func(g accessGrant) (string, string, bool) {
		o, ok := desired[g]
		return o.intent, o.assignment, ok
	}, desiredAll)

	report := AccessRecertification{
		View: view, CadenceDays: accessRecertCadenceDays,
		GrantSetHash: hashGrantReviews(reviews), Grants: reviews,
		Status: AccessRecertificationStatus(Overdue),
	}
	// last attested = the newest access.recertify audit event for this View.
	last, ok, err := s.Store.LatestAuditForObject(ctx, accessRecertAction, "view:"+view)
	if err != nil {
		return AccessRecertification{}, err
	}
	if ok {
		at := last.At
		report.LastAttested = &at
		if last.PrincipalID != "" {
			pid := last.PrincipalID
			report.LastAttestedBy = &pid
		}
		if time.Since(at) <= time.Duration(accessRecertCadenceDays)*24*time.Hour {
			report.Status = AccessRecertificationStatus(Current)
		}
	}
	return report, nil
}

// buildGrantReviews unions observed and desired grants into a sorted review
// list: every observed tuple (with hosts + managed flag) plus every desired
// tuple not observed anywhere (managed, zero hosts — a grant that has drifted
// off its hosts, still under review).
func buildGrantReviews(hostsByGrant map[accessGrant]map[string]bool, lookup func(accessGrant) (string, string, bool), desiredAll []accessGrant) []AccessGrantReview {
	seen := map[accessGrant]bool{}
	var out []AccessGrantReview
	add := func(g accessGrant, hosts []string) {
		intent, assignment, managed := lookup(g)
		rv := AccessGrantReview{
			Subject: g.Subject, Kind: AccessGrantReviewKind(g.Kind), Scope: g.Scope,
			Hosts: hosts, Managed: managed,
		}
		if managed {
			rv.Intent = strPtr(intent)
			rv.Assignment = strPtr(assignment)
		}
		out = append(out, rv)
		seen[g] = true
	}
	for g, hostSet := range hostsByGrant {
		hosts := make([]string, 0, len(hostSet))
		for h := range hostSet {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		add(g, hosts)
	}
	for _, g := range desiredAll {
		if !seen[g] {
			add(g, []string{})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// hashGrantReviews computes the attestation anchor: sha256 over the canonical
// (sorted) grant set including hosts + managed, so any change to who-has-what
// invalidates a prior attestation (§1.8).
func hashGrantReviews(reviews []AccessGrantReview) string {
	h := sha256.New()
	for _, rv := range reviews {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\x00%s\n",
			rv.Subject, rv.Kind, rv.Scope, rv.Managed, strings.Join(rv.Hosts, ","))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func specString(spec map[string]any, key string) string {
	if v, ok := spec[key].(string); ok {
		return v
	}
	return ""
}

func strPtr(s string) *string { return &s }
