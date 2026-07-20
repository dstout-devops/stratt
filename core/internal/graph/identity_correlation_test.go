package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestCorrelateIdentities proves ADR-0079 slice 4a: a credential Entity `identifies`
// the subject it attests (cross-source: PKI cert × IdP user), and a credential
// attesting a DEACTIVATED identity raises the leaver Finding — the query no island
// model could answer.
func TestCorrelateIdentities(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Subjects: alice active, bob deactivated at the IdP.
	if err := store.UpsertIDP(ctx, types.SCIMIdP{Name: "okta", TokenHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIdentity(ctx, types.SCIMIdentity{IDP: "okta", SCIMID: "u1", UserName: "alice", PrincipalID: "alice@corp", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIdentity(ctx, types.SCIMIdentity{IDP: "okta", SCIMID: "u2", UserName: "bob", PrincipalID: "bob@corp", Active: false}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureIdentitySubjectOwner(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectSCIMEntities(ctx); err != nil {
		t.Fatalf("project subjects: %v", err)
	}

	// Two client certs (as the certissuer plugin would project): one for alice, one
	// for the leaver bob. Register the identity.credential owner (the cert connector).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "identity.credential", OwnerKind: string(types.WriterSyncer), OwnerRef: "certissuer",
	}); err != nil {
		t.Fatal(err)
	}
	src, err := store.RegisterSource(ctx, types.Source{Kind: "certissuer", Name: "pki"})
	if err != nil {
		t.Fatal(err)
	}
	proj := store.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "certissuer", SourceID: src.ID}
	if _, err := proj.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "cert", IdentityKeys: map[string]string{"cert.serial": "S-ALICE"},
			Facets: map[string]json.RawMessage{"identity.credential": cred("alice@corp")}},
		{Kind: "cert", IdentityKeys: map[string]string{"cert.serial": "S-BOB"},
			Facets: map[string]json.RawMessage{"identity.credential": cred("bob@corp")}},
	}); err != nil {
		t.Fatalf("project certs: %v", err)
	}

	if err := store.CorrelateIdentities(ctx); err != nil {
		t.Fatalf("correlate: %v", err)
	}

	// Both certs identify their subject: two `identifies` edges.
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='identifies'`); got != 2 {
		t.Fatalf("want 2 identifies edges, got %d", got)
	}
	// Exactly one leaver Finding — bob's cert (alice is active, no Finding).
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='identity/leaver-credential' AND status='open'`); got != 1 {
		t.Fatalf("want 1 open leaver-credential Finding, got %d", got)
	}

	// Idempotent: re-correlate → same edges + one Finding (no duplicates).
	if err := store.CorrelateIdentities(ctx); err != nil {
		t.Fatalf("re-correlate: %v", err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='identifies'`); got != 2 {
		t.Fatalf("after re-correlate want 2 identifies, got %d", got)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='identity/leaver-credential' AND status='open'`); got != 1 {
		t.Fatalf("after re-correlate want 1 leaver Finding, got %d", got)
	}
}

// TestCorrelateIdentities_Service proves ADR-0081 slice 3: a service's mTLS cert
// `identifies` the service Entity when the cert's CN or SAN matches the service's
// dns.fqdn — the identity↔service cross-dimension link. DB-gated.
func TestCorrelateIdentities_Service(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// A `service` Entity carrying its K8s DNS name (as kubeservices projects it).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "service.endpoint", OwnerKind: string(types.WriterSyncer), OwnerRef: "kubeservices",
	}); err != nil {
		t.Fatal(err)
	}
	svcSrc, _ := store.RegisterSource(ctx, types.Source{Kind: "kubeservices", Name: "k8s"})
	ep, _ := json.Marshal(map[string]any{"ports": []map[string]any{{"port": 8443}}})
	svcProv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "kubeservices", SourceID: svcSrc.ID}
	svcIDs, err := store.NormalizerProjector().UpsertEntities(ctx, svcProv, []EntityUpsert{{
		Kind: "service",
		IdentityKeys: map[string]string{
			"k8s.service": "prod/web",
			"dns.fqdn":    "web.prod.svc.cluster.local",
		},
		Facets: map[string]json.RawMessage{"service.endpoint": ep},
	}})
	if err != nil {
		t.Fatal(err)
	}
	serviceID := svcIDs[0]

	// A cert whose SAN is the service FQDN (a service mTLS cert).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "identity.credential", OwnerKind: string(types.WriterSyncer), OwnerRef: "certissuer",
	}); err != nil {
		t.Fatal(err)
	}
	pkiSrc, _ := store.RegisterSource(ctx, types.Source{Kind: "certissuer", Name: "pki"})
	credDoc, _ := json.Marshal(map[string]any{
		"scheme": "cert", "subjectName": "web", "notAfter": "2027-01-01T00:00:00Z",
		"subjectAltNames": []string{"web.prod.svc.cluster.local"},
	})
	if _, err := store.NormalizerProjector().UpsertEntities(ctx,
		types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "certissuer", SourceID: pkiSrc.ID},
		[]EntityUpsert{{Kind: "cert", IdentityKeys: map[string]string{"cert.serial": "S-web"},
			Facets: map[string]json.RawMessage{"identity.credential": credDoc}}}); err != nil {
		t.Fatal(err)
	}

	if err := store.CorrelateIdentities(ctx); err != nil {
		t.Fatalf("correlate: %v", err)
	}

	// The cert identifies the service (matched by the SAN → dns.fqdn), and no leaver
	// Finding (a service is not a disabled user).
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='identifies' AND to_id='`+serviceID+`'`); got != 1 {
		t.Fatalf("the cert must `identifies` the service via its FQDN SAN, got %d edges", got)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='identity/leaver-credential'`); got != 0 {
		t.Fatalf("a service cert must not raise a leaver Finding, got %d", got)
	}
}

func cred(subjectName string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"scheme":       "cert",
		"subjectName":  subjectName,
		"issuer":       "Corp CA",
		"serialNumber": "S-" + subjectName,
		"notAfter":     "2027-01-01T00:00:00Z",
	})
	return raw
}
