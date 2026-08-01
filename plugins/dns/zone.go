package dns

import (
	"context"
	"fmt"
	"strings"
	"time"

	miekg "github.com/miekg/dns"
)

// zoneClient is the transport seam. RFC 2136 is a TRANSPORT beneath our contract
// (§1.5), so it lives behind an interface: a cloud-API DNS provider is the same three
// operations against a REST endpoint, and the unit suite drives a fake with no server
// anywhere.
type zoneClient interface {
	// Transfer enumerates a zone (AXFR). A whole-zone read, because AXFR has no delta
	// form and inventing a cursor over a protocol that has none would be a lie the
	// core would then trust for tombstoning.
	Transfer(ctx context.Context, zone string) ([]Record, error)
	// Update writes ONE record, converging: the RRset is removed and re-added, so a
	// re-run lands on the same zone state (RFC 2136 §2.5.1/§2.5.2) — which is what
	// lets the Action advertise Idempotent and the fleet Apply re-run on a cadence.
	Update(ctx context.Context, zone string, rec Record) error
	// Remove deletes an RRset by (name, type). By RRset and never by value: removing
	// "the A record whose data is X" silently does nothing once the data has drifted,
	// which is exactly when a removal matters.
	Remove(ctx context.Context, zone, name, rtype string) error
}

// TSIGKey is the shared secret this plugin signs UPDATE and AXFR with. Material only —
// it never reaches a declaration, a log line, or the graph (§2.5); it arrives in the
// pod's environment from a mounted Secret, the way every other plugin credential does.
type TSIGKey struct {
	Name      string // the key name, as the server knows it
	Secret    string // base64, as `tsig-keygen` emits
	Algorithm string // hmac-sha256 (default) | hmac-sha512 | hmac-sha384 | hmac-sha224 | hmac-sha1
}

// configured reports whether a key was supplied at all. Unsigned updates are refused
// rather than attempted: a server that accepts them is misconfigured, and a server that
// rejects them fails at the far end of a Run with a REFUSED nobody can read (§1.8).
func (k TSIGKey) configured() bool { return k.Name != "" && k.Secret != "" }

// algorithm maps the operator's spelling to the wire constant, defaulting to SHA-256.
// SHA-1 is accepted because estates still run it; it is not the default.
func (k TSIGKey) algorithm() (string, error) {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(k.Algorithm), ".")) {
	case "", "hmac-sha256":
		return miekg.HmacSHA256, nil
	case "hmac-sha512":
		return miekg.HmacSHA512, nil
	case "hmac-sha384":
		return miekg.HmacSHA384, nil
	case "hmac-sha224":
		return miekg.HmacSHA224, nil
	case "hmac-sha1":
		return miekg.HmacSHA1, nil
	default:
		return "", fmt.Errorf("unsupported TSIG algorithm %q", k.Algorithm)
	}
}

// keyName is the TSIG key name on the wire — always fully qualified.
func (k TSIGKey) keyName() string { return miekg.Fqdn(strings.ToLower(k.Name)) }

// rfc2136 is the real transport: dynamic UPDATE for writes, AXFR for reads, TSIG on
// both, TCP throughout (an UPDATE or a transfer over UDP is a truncation waiting to
// happen).
type rfc2136 struct {
	server string
	key    TSIGKey
}

const exchangeTimeout = 15 * time.Second

func (c *rfc2136) exchange(ctx context.Context, m *miekg.Msg) (*miekg.Msg, error) {
	if !c.key.configured() {
		return nil, fmt.Errorf("no TSIG key configured — this provider signs every UPDATE and AXFR (§2.5); an unsigned write would be refused by any correctly-configured server")
	}
	alg, err := c.key.algorithm()
	if err != nil {
		return nil, err
	}
	cl := &miekg.Client{
		Net:        "tcp",
		Timeout:    exchangeTimeout,
		TsigSecret: map[string]string{c.key.keyName(): c.key.Secret},
	}
	m.SetTsig(c.key.keyName(), alg, 300, time.Now().Unix())
	resp, _, err := cl.ExchangeContext(ctx, m, hostPort(c.server))
	if err != nil {
		return nil, err
	}
	if resp.Rcode != miekg.RcodeSuccess {
		return nil, fmt.Errorf("server answered %s", miekg.RcodeToString[resp.Rcode])
	}
	return resp, nil
}

func (c *rfc2136) Update(ctx context.Context, zone string, rec Record) error {
	rrs, err := toRRs(rec)
	if err != nil {
		return err
	}
	m := new(miekg.Msg)
	m.SetUpdate(miekg.Fqdn(strings.ToLower(zone)))
	// Remove-then-insert in ONE message: RFC 2136 applies an update atomically, so the
	// name is never briefly absent — which matters when the thing being re-registered
	// is how the estate reaches a running machine.
	m.RemoveRRset(rrs)
	m.Insert(rrs)
	_, err = c.exchange(ctx, m)
	return err
}

func (c *rfc2136) Remove(ctx context.Context, zone, name, rtype string) error {
	rr, err := emptyRR(name, rtype)
	if err != nil {
		return err
	}
	m := new(miekg.Msg)
	m.SetUpdate(miekg.Fqdn(strings.ToLower(zone)))
	m.RemoveRRset([]miekg.RR{rr})
	_, err = c.exchange(ctx, m)
	return err
}

func (c *rfc2136) Transfer(ctx context.Context, zone string) ([]Record, error) {
	if !c.key.configured() {
		return nil, fmt.Errorf("no TSIG key configured — this Syncer signs its AXFR (§2.5)")
	}
	alg, err := c.key.algorithm()
	if err != nil {
		return nil, err
	}
	t := &miekg.Transfer{TsigSecret: map[string]string{c.key.keyName(): c.key.Secret}}
	m := new(miekg.Msg)
	m.SetAxfr(miekg.Fqdn(strings.ToLower(zone)))
	m.SetTsig(c.key.keyName(), alg, 300, time.Now().Unix())
	ch, err := t.In(m, hostPort(c.server))
	if err != nil {
		return nil, err
	}
	var out []Record
	for env := range ch {
		if env.Error != nil {
			return nil, env.Error
		}
		for _, rr := range env.RR {
			if r, ok := fromRR(rr); ok {
				out = append(out, r)
			} else {
				// Not a host coordinate (SOA/NS/MX/TXT/…). Carried as a Record with an
				// unrecognised Type so normalizeZone can COUNT what it declined to
				// project — a projection that silently drops most of its input should
				// be able to say how much (§1.8).
				out = append(out, Record{Name: strings.ToLower(strings.TrimSuffix(rr.Header().Name, ".")), Type: "-"})
			}
		}
	}
	return out, nil
}

// toRRs renders one Record as the wire RRs an UPDATE carries.
func toRRs(rec Record) ([]miekg.RR, error) {
	rr, err := miekg.NewRR(fmt.Sprintf("%s. %d IN %s %s", rec.Name, rec.TTL, rec.Type, rrData(rec)))
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", rec, err)
	}
	return []miekg.RR{rr}, nil
}

// rrData qualifies a CNAME's target on the wire — a bare name in an RR is relative to
// the zone, which is not what "point at this canonical name" means.
func rrData(rec Record) string {
	if rec.Type == "CNAME" {
		return miekg.Fqdn(rec.Data)
	}
	return rec.Data
}

// emptyRR builds the header-only RR an RRset deletion needs (RFC 2136 §2.5.2: class
// ANY, TTL 0, no rdata).
func emptyRR(name, rtype string) (miekg.RR, error) {
	t, ok := miekg.StringToType[rtype]
	if !ok {
		return nil, fmt.Errorf("unknown record type %q", rtype)
	}
	return &miekg.RFC3597{Hdr: miekg.RR_Header{
		Name: miekg.Fqdn(strings.ToLower(name)), Rrtype: t, Class: miekg.ClassANY, Ttl: 0,
	}}, nil
}

// fromRR maps a transferred RR to a Record, for the three types that name a host.
func fromRR(rr miekg.RR) (Record, bool) {
	name := strings.ToLower(strings.TrimSuffix(rr.Header().Name, "."))
	switch v := rr.(type) {
	case *miekg.A:
		return Record{Name: name, Type: "A", Data: v.A.String(), TTL: rr.Header().Ttl}, true
	case *miekg.AAAA:
		return Record{Name: name, Type: "AAAA", Data: v.AAAA.String(), TTL: rr.Header().Ttl}, true
	case *miekg.CNAME:
		return Record{Name: name, Type: "CNAME", Data: strings.ToLower(strings.TrimSuffix(v.Target, ".")), TTL: rr.Header().Ttl}, true
	}
	return Record{}, false
}
