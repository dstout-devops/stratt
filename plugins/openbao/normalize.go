// Package openbao is the tool-named home for the OpenBao-backed surfaces (ADR-0098).
// It implements the NEUTRAL cert-issuer Contract (§1.5): the Vault-compatible PKI
// content-expertise behind the sovereign plugin port (ADR-0046) — a Syncer (Observe
// issued certs + the CA hierarchy), a reconcile Actuator (Plan/Apply/Destroy the cert
// lifecycle via born-on-target CSR/sign, ADR-0050), and administrative PKI Actions
// (create-intermediate, rotate-crl). It maps X.509 certs to core-legible
// ObservedEntity wire values; the core-side host governs what it may write. The plugin
// holds no graph write path (§1.2).
package openbao

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// normalizeCert maps one issued leaf certificate onto an ObservedEntity — the
// minimum demanded by the cert.identity / cert.expiry Facet schemas (§1.1). Pure
// content-expertise; the plugin only proposes, it never writes (§1.2). Identity is
// cert.serial, the label is cert.commonName, facets are cert.identity /
// cert.expiry.
//
// ok=false (with no error) means "do not project this serial": CA/self-signed
// certs are the issuer, not estate certificates. A serial whose PEM fails to parse
// returns an error and is skipped loudly by the caller (§1.8).
func normalizeCert(c Cert) (entity *pluginv1.ObservedEntity, ok bool, err error) {
	block, _ := pem.Decode([]byte(c.PEM))
	if block == nil {
		return nil, false, fmt.Errorf("openbao: serial %s: no PEM block", c.Serial)
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false, fmt.Errorf("openbao: serial %s: parse: %w", c.Serial, err)
	}
	// The CA root/intermediate is the issuer, not an estate leaf cert (§2.4:
	// Intent/Certificate governs issued certs, not the authority itself).
	if crt.IsCA {
		return nil, false, nil
	}

	identity := map[string]string{"cert.serial": c.Serial}
	labels := map[string]string{"cert.commonName": crt.Subject.CommonName}

	certIdentity := map[string]any{
		"commonName":   crt.Subject.CommonName,
		"serialNumber": c.Serial,
		"issuer":       crt.Issuer.CommonName,
	}
	if len(crt.DNSNames) > 0 {
		certIdentity["dnsNames"] = crt.DNSNames
	}
	certExpiry := map[string]any{
		"notBefore": crt.NotBefore.UTC().Format(time.RFC3339),
		"notAfter":  crt.NotAfter.UTC().Format(time.RFC3339),
	}

	// identity.credential (ADR-0079 slice 2): the CROSS-FORM projection under which
	// a cert is queryable alongside every other credential form (key, token) — e.g.
	// "all credentials expiring in 30 days". A cert is not an identity island; it is
	// a credential FORM. cert.identity/cert.expiry stay (more signal, not less); this
	// is the unifying view demanded by the cert reconcile Contract (ADR-0050). No
	// secret material (§2.5).
	identityCredential := map[string]any{
		"scheme":       "cert",
		"subjectName":  crt.Subject.CommonName,
		"issuer":       crt.Issuer.CommonName,
		"serialNumber": c.Serial,
		"notBefore":    crt.NotBefore.UTC().Format(time.RFC3339),
		"notAfter":     crt.NotAfter.UTC().Format(time.RFC3339),
		"algorithm":    crt.SignatureAlgorithm.String(),
	}
	if len(crt.DNSNames) > 0 {
		identityCredential["subjectAltNames"] = crt.DNSNames
	}

	facets := map[string][]byte{}
	for ns, doc := range map[string]map[string]any{
		"cert.identity":       certIdentity,
		"cert.expiry":         certExpiry,
		"identity.credential": identityCredential,
	} {
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, false, fmt.Errorf("openbao: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return &pluginv1.ObservedEntity{
		Kind:         "cert",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, true, nil
}

// normalizeCA maps a CA certificate onto a `ca` Entity — the CA-hierarchy observation
// (ADR-0098 E2). Identity is pki.caSerial; the closed ca.config Facet carries the CA's
// commonName / notAfter / isCA. ok=false (no error) for a non-CA cert (a leaf goes
// through normalizeCert instead). Pure content-expertise; no material (§2.5).
func normalizeCA(c Cert) (*pluginv1.ObservedEntity, bool, error) {
	block, _ := pem.Decode([]byte(c.PEM))
	if block == nil {
		return nil, false, fmt.Errorf("openbao: ca serial %s: no PEM block", c.Serial)
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false, fmt.Errorf("openbao: ca serial %s: parse: %w", c.Serial, err)
	}
	if !crt.IsCA {
		return nil, false, nil // a leaf — normalizeCert handles it
	}
	caConfig := map[string]any{
		"commonName": crt.Subject.CommonName,
		"notAfter":   crt.NotAfter.UTC().Format(time.RFC3339),
		"isCA":       true,
	}
	raw, err := json.Marshal(caConfig)
	if err != nil {
		return nil, false, fmt.Errorf("openbao: marshal ca.config %s: %w", c.Serial, err)
	}
	return &pluginv1.ObservedEntity{
		Kind:         "ca",
		IdentityKeys: map[string]string{"pki.caSerial": c.Serial},
		Labels:       map[string]string{"cert.commonName": crt.Subject.CommonName},
		Facets:       map[string][]byte{"ca.config": raw},
	}, true, nil
}

// normalizeSecret maps a KV secret's METADATA onto a `secret` Entity (ADR-0099). Identity
// is kv.path = mount/path; the closed kv.metadata Facet carries version + timestamps —
// NEVER the secret value (§1.2/§2.5: the graph records that the secret exists, not what
// it is). This is the observed external secret's metadata, NOT a CredentialRef (which is
// Stratt's Git-declared desired-state pointer).
func normalizeSecret(mount, path string, md KVMetadata) *pluginv1.ObservedEntity {
	doc := map[string]any{
		"mount":          mount,
		"path":           path,
		"currentVersion": md.CurrentVersion,
	}
	if md.CreatedTime != "" {
		doc["createdTime"] = md.CreatedTime
	}
	if md.UpdatedTime != "" {
		doc["updatedTime"] = md.UpdatedTime
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return &pluginv1.ObservedEntity{
		Kind:         "kv-secret", // hyphenated to harden the disambiguation from CredentialRef
		IdentityKeys: map[string]string{"kv.path": mount + "/" + path},
		Labels:       map[string]string{"kv.mount": mount},
		Facets:       map[string][]byte{"kv.metadata": raw},
	}
}
