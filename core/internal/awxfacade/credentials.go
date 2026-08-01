package awxfacade

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// ── credentials: the AWX Credentials resource, served from CredentialRefs ────────────────────────
//
// An AWX credential and a Stratt CredentialRef are the same OBJECT in an inventory-of-things sense
// and NOT the same thing at all in substance, which is what makes this family worth writing
// carefully rather than mechanically.
//
// An AWX credential CONTAINS material: encrypted fields in AWX's own database, decrypted into the
// job at runtime. A Stratt CredentialRef contains a POINTER — an owner team, a broker backend, a
// locator addressing material inside that backend, and a per-field injection policy. Material never
// enters the platform (§2.5, ADR-0009), and there is no method anywhere in the graph store that
// returns any: not a redacted one, not an encrypted one, none.
//
// ── WHAT `inputs` CARRIES, AND WHY IT IS NOT EMPTY ──────────────────────────────────────────────
// AWX renders `inputs` as the declared fields with secret values replaced by the literal
// `$encrypted$`. The faithful projection here is the same shape built from the ref's INJECTION KEYS:
// those key names are declared in Git, reviewed there, and already served on /api/v1 — they are not
// secret, and hiding them would hide the diagnosis (§1.8) of "which fields does this ref project?"
// while protecting nothing.
//
// `$encrypted$` is the honest value: it asserts "a secret stands behind this key", which is exactly
// true. It does NOT assert that Stratt holds it. What Stratt holds is on the descent block.
//
// The one thing that must never happen is a value here that came from material, and there is no way
// to write one: the only input to this function is types.CredentialRef, which has no material field.
// TestCredentialInputsAreKeyNamesNeverValues pins the rendering; the type system pins the rest.
//
// ── ATTACHING A CREDENTIAL AT LAUNCH IS STILL NOT HONORED ───────────────────────────────────────
// `launchBody.Credentials` continues to land in `ignored_fields`. A Step's credentialRefs are
// DECLARED (ADR-0009): the Workflow says which refs it uses, review says who may, and the use-check
// runs against the launching Principal. Letting a launch swap in a different credential id would
// make the compat surface the one door where that review is skipped. Listing them is discovery;
// binding them is desired state.

// credentialTypeName is the single synthetic AWX credential_type every Stratt CredentialRef reports.
//
// One type, not one per backend, because AWX's credential_type describes what a credential IS FOR
// (ssh, aws, vault) while Stratt's `backend` describes WHO BROKERS IT (k8s-secret, vault,
// workload-identity). Those are different axes, and mapping one onto the other would be a category
// error that reads as fact. Every Stratt credential is the same kind of thing — a brokered pointer —
// so it gets one type, and the backend rides the descent block where it means what it says.
const credentialTypeName = "Stratt CredentialRef"

// credentialTypeID is the type's synthetic id. It resolves: /api/v2/credential_types/{id}/ serves
// it. A dangling type reference was the alternative and this package does not ship those.
func credentialTypeID() int64 { return awxID(credentialTypeName) }

// credentialType renders the one custom type. `kind` is "cloud" not by choice but by constraint:
// AWX permits only "cloud" or "net" on a CUSTOM credential type, so there is no more faithful value
// available, and the description says what the object really is.
func credentialType() map[string]any {
	id := credentialTypeID()
	return map[string]any{
		"id":      id,
		"type":    "credential_type",
		"name":    credentialTypeName,
		"kind":    "cloud",
		"managed": false,
		"description": "A brokered pointer to material held outside the platform (Stratt §2.5): an owner " +
			"team, a broker backend, a locator, and a per-field injection policy. Stratt never holds the " +
			"material itself, so no field here is decryptable — the projected field NAMES appear on each " +
			"credential's inputs, and the broker coordinates on its summary_fields.stratt block.",
		"url": jt("/api/v2/credential_types/%d/", id),
		// FIELDS ARE INTENTIONALLY EMPTY. AWX puts a fixed field schema on the type and every
		// credential of that type answers it. A CredentialRef's projected fields are declared
		// PER REF in Git, so there is no per-type schema to publish — and inventing a union of
		// every ref's keys would describe a type no ref actually has.
		"inputs":    map[string]any{"fields": []any{}},
		"injectors": map[string]any{},
	}
}

// credentialRefToCredential renders a CredentialRef as an AWX credential.
func credentialRefToCredential(ref types.CredentialRef) map[string]any {
	id := awxID(ref.Name)
	// The declared field names, with AWX's own "a secret stands here" sentinel. Never a value —
	// there is no value in scope to write, by construction.
	inputs := map[string]any{}
	for _, inj := range ref.Injection {
		if inj.Key != "" {
			inputs[inj.Key] = "$encrypted$"
		}
	}
	desc := fmt.Sprintf("Stratt CredentialRef %q brokered by %s, owned by team %s",
		ref.Name, ref.Backend, ref.OwnerTeam)
	if ref.GateOnly || len(ref.Injection) == 0 {
		// A gate-only ref brokers NOTHING (ADR-0052/0092) — it is purely an authorization gate on
		// an Action. Saying so beats an empty `inputs` a reader would take for "not yet configured".
		desc += " — GATE-ONLY: brokers no material at all, it authorizes an Action and nothing more"
	}
	return map[string]any{
		"id":              id,
		"type":            "credential",
		"name":            ref.Name,
		"description":     desc,
		"organization":    nil, // Stratt scopes by owner TEAM; there is no org object to point at
		"credential_type": credentialTypeID(),
		"kind":            "cloud",
		"managed":         false,
		"inputs":          inputs,
		"url":             jt("/api/v2/credentials/%d/", id),
		"summary_fields": map[string]any{
			"credential_type":   map[string]any{"id": credentialTypeID(), "name": credentialTypeName},
			"user_capabilities": map[string]bool{"edit": false, "delete": false, "use": true},
			// The descent block: what Stratt actually holds. The locator is deliberately ABSENT —
			// it is not material, but it is the address OF material, and a compat listing is not
			// the place to widen who reads it. /api/v1 serves it under its own authz.
			"stratt": map[string]any{
				"backend":     ref.Backend,
				"owner_team":  ref.OwnerTeam,
				"declared_by": ref.DeclaredBy,
				"gate_only":   ref.GateOnly || len(ref.Injection) == 0,
				"injects":     injectionSummary(ref.Injection),
			},
		},
	}
}

// injectionSummary reports HOW each field is projected (env var or file), which is the operational
// question an AWX-side reader actually has. Names and modes only; the type carries nothing else.
func injectionSummary(injs []types.CredentialInjection) []map[string]any {
	out := make([]map[string]any, 0, len(injs))
	for _, inj := range injs {
		out = append(out, map[string]any{"key": inj.Key, "as": inj.As, "name": inj.Name})
	}
	return out
}

// listCredentials: GET /api/v2/credentials/.
func (f *Facade) listCredentials(w http.ResponseWriter, r *http.Request) {
	refs, err := f.cfg.Store.ListCredentialRefs(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(refs))
	for _, ref := range refs {
		items = append(items, named{id: awxID(ref.Name), name: ref.Name, obj: credentialRefToCredential(ref)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getCredential: GET /api/v2/credentials/{id}/.
func (f *Facade) getCredential(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	refs, err := f.cfg.Store.ListCredentialRefs(r.Context())
	if err != nil {
		if errors.Is(err, graph.ErrNotFound) {
			awxErr(w, http.StatusNotFound, "Not found.")
			return
		}
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, ref := range refs {
		if awxID(ref.Name) == id {
			writeJSON(w, http.StatusOK, credentialRefToCredential(ref))
			return
		}
	}
	awxErr(w, http.StatusNotFound, "Not found.")
}

// listCredentialTypes: GET /api/v2/credential_types/ — the one synthetic type.
func (f *Facade) listCredentialTypes(w http.ResponseWriter, r *http.Request) {
	items := []named{{id: credentialTypeID(), name: credentialTypeName, obj: credentialType()}}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getCredentialType: GET /api/v2/credential_types/{id}/.
func (f *Facade) getCredentialType(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok || id != credentialTypeID() {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	writeJSON(w, http.StatusOK, credentialType())
}
