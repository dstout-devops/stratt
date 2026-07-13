package certissuer

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// normalizeCert maps one issued leaf certificate onto the graph shape — the
// minimum demanded by the cert.identity / cert.expiry Facet schemas (§1.1).
// Pure function; the Syncer routes the result through the Projector.
//
// ok=false (with no error) means "do not project this serial": CA/self-signed
// certs are the issuer, not estate certificates, and a serial whose PEM fails
// to parse is skipped loudly by the caller.
func normalizeCert(c Cert) (up graph.EntityUpsert, ok bool, err error) {
	block, _ := pem.Decode([]byte(c.PEM))
	if block == nil {
		return up, false, fmt.Errorf("certissuer: serial %s: no PEM block", c.Serial)
	}
	crt, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return up, false, fmt.Errorf("certissuer: serial %s: parse: %w", c.Serial, err)
	}
	// The CA root/intermediate is the issuer, not an estate leaf cert (§2.4:
	// Intent/Certificate governs issued certs, not the authority itself).
	if crt.IsCA {
		return up, false, nil
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

	facets := map[string]json.RawMessage{}
	for ns, doc := range map[string]map[string]any{
		"cert.identity": certIdentity,
		"cert.expiry":   certExpiry,
	} {
		raw, err := json.Marshal(doc)
		if err != nil {
			return up, false, fmt.Errorf("certissuer: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return graph.EntityUpsert{
		Kind:         "cert",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, true, nil
}
