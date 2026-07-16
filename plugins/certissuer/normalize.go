// Package certissuer is the cert-issuer Connector plugin: the Vault-compatible
// PKI content-expertise that used to live in core/internal/connectors/certissuer
// (the Syncer) and core/internal/actions/certissuer (the issue/renew/revoke
// Actions), now behind the sovereign plugin port (ADR-0046). It maps issued X.509
// certs to core-legible ObservedEntity wire values and runs the three write ops;
// the core-side host governs what it may write (ownership, identity gating, Run
// provenance). The plugin holds no graph write path (§1.2).
package certissuer

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
		return nil, false, fmt.Errorf("certissuer: serial %s: no PEM block", c.Serial)
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, false, fmt.Errorf("certissuer: serial %s: parse: %w", c.Serial, err)
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

	facets := map[string][]byte{}
	for ns, doc := range map[string]map[string]any{
		"cert.identity": certIdentity,
		"cert.expiry":   certExpiry,
	} {
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, false, fmt.Errorf("certissuer: marshal facet %s: %w", ns, err)
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
