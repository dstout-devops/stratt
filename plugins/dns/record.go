package dns

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Record is the plugin's own minimal record shape — the three RR types that name a
// host, and nothing else. Deliberately NOT a DNS ontology (§1.1): this plugin's job is
// a reach coordinate, not a zone editor, and every field here is demanded by that job.
type Record struct {
	Name string // fully-qualified, lowercased, no trailing dot
	Type string // A | AAAA | CNAME
	Data string // an address for A/AAAA; a canonical name for CNAME
	TTL  uint32
}

func (r Record) String() string {
	return fmt.Sprintf("%s %d %s %s", r.Name, r.TTL, r.Type, r.Data)
}

// defaultTTL is short on purpose: these records track machines that get rebuilt, and a
// long TTL turns "the estate re-registered this name" into "the estate re-registered
// this name and half the fleet will not notice for a day".
const defaultTTL = 300

// canonical qualifies and normalizes a record name against its zone. DNS is
// case-insensitive and the graph should not be, so everything is lowercased once, here.
func canonical(zone, name string) (string, error) {
	z := strings.ToLower(strings.Trim(strings.TrimSpace(zone), "."))
	n := strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	if z == "" {
		return "", fmt.Errorf("zone is required")
	}
	if n == "" {
		return "", fmt.Errorf("record name is required")
	}
	if strings.ContainsAny(n, " \t\r\n") {
		return "", fmt.Errorf("record name %q contains whitespace", name)
	}
	if n == z || strings.HasSuffix(n, "."+z) {
		return n, nil
	}
	if strings.Contains(n, ".") {
		// A qualified name from a DIFFERENT zone is refused rather than silently
		// re-suffixed into this one: RFC 2136 updates are scoped to a zone, and
		// "www.other.example" quietly becoming "www.other.example.ours.example" is a
		// record nobody asked for at a name nobody will look at (§1.8).
		return "", fmt.Errorf("name %q is qualified but not in zone %q", name, z)
	}
	return n + "." + z, nil
}

// supportedType normalizes and gates the RR type. The gate is the point: an unsupported
// type is refused at the seam rather than written and then invisible to the Syncer,
// which projects only these three.
func supportedType(t string) (string, error) {
	switch up := strings.ToUpper(strings.TrimSpace(t)); up {
	case "A", "AAAA", "CNAME":
		return up, nil
	case "":
		return "", fmt.Errorf("record type is required (A, AAAA or CNAME)")
	default:
		return "", fmt.Errorf("record type %q is not supported — this provider writes the types that name a host (A, AAAA, CNAME), not a zone editor's full set", up)
	}
}

// newRecord validates a DECLARED record: the estate said the name, the type and the
// data, and each has to agree with the others. An A whose data is not an IPv4 address
// is refused here rather than by the DNS server three hops later.
func newRecord(zone, name, rtype, data string, ttl uint32) (Record, error) {
	n, err := canonical(zone, name)
	if err != nil {
		return Record{}, err
	}
	t, err := supportedType(rtype)
	if err != nil {
		return Record{}, err
	}
	d := strings.TrimSpace(data)
	if d == "" {
		return Record{}, fmt.Errorf("record data is required for %s %s", n, t)
	}
	switch t {
	case "A", "AAAA":
		addr, perr := netip.ParseAddr(d)
		if perr != nil {
			return Record{}, fmt.Errorf("%s record %s: data %q is not an IP address", t, n, data)
		}
		if t == "A" && !addr.Is4() {
			return Record{}, fmt.Errorf("A record %s: %q is IPv6 — use AAAA", n, data)
		}
		if t == "AAAA" && addr.Is4() {
			return Record{}, fmt.Errorf("AAAA record %s: %q is IPv4 — use A", n, data)
		}
		d = addr.String()
	case "CNAME":
		d = strings.ToLower(strings.Trim(d, "."))
		if _, perr := netip.ParseAddr(d); perr == nil {
			return Record{}, fmt.Errorf("CNAME %s: data %q is an address, not a name", n, data)
		}
		if d == n {
			// A self-referential CNAME is a resolver loop, and it is the shape this
			// design walks past: the estate's name and the substrate's name are equal
			// when the zones coincide. Refused loudly — a loop is not a no-op.
			return Record{}, fmt.Errorf("CNAME %s points at itself — the estate name and the target's own coordinate are the same name, so there is nothing to alias", n)
		}
	}
	if ttl == 0 {
		ttl = defaultTTL
	}
	return Record{Name: n, Type: t, Data: d, TTL: ttl}, nil
}

// recordForTarget builds the record for ONE fleet target: the estate's name for it, in
// the estate's zone, pointing at the coordinate the core resolved from the graph.
//
// The A-vs-CNAME branch is a fact about the value in hand, not a policy: an address is
// an address and a name is a name. This is the whole reason no IP is declared in Git —
// the data comes from what was observed, never from what was written down.
func recordForTarget(zone, targetName, coordinate string, ttl uint32) (Record, error) {
	if _, err := netip.ParseAddr(coordinate); err == nil {
		t := "A"
		if strings.Contains(coordinate, ":") {
			t = "AAAA"
		}
		return newRecord(zone, targetName, t, coordinate, ttl)
	}
	return newRecord(zone, targetName, "CNAME", coordinate, ttl)
}

// normalizeZone maps the records of one zone to the Entities they describe. This is the
// projection rule of ADR-0144 D3, and it has no judgement in it — which is the point,
// because the alternative is guessing which host a record "means":
//
//	A / AAAA  →  the record's own name is canonical; the Entity IS that name.
//	CNAME     →  the record's name is an ALIAS; the Entity is the CANONICAL TARGET,
//	             and the alias is an additional coordinate for it. That is what makes
//	             the estate's stable name land on the machine the substrate named
//	             something else.
//	anything else → not a host coordinate; not projected at all.
//
// `kind` is the operator's, not the plugin's, and that is load-bearing:
// upsertEntityTx SETS `kind` unconditionally when a projection correlates onto an
// existing Entity, so a Syncer that guessed here would silently RETYPE another source's
// Entity — a `vm` becoming a `dns-record` and dropping out of every View that selects
// vms. The zone is the estate's naming domain, so the estate says what its names denote.
//
// Returns the entities and the count of records that were not coordinates (for the log:
// a zone is mostly SOA/NS/MX/TXT, and a projection that silently ignored most of its
// input should say how much).
func normalizeZone(kind string, records []Record) ([]*pluginv1.ObservedEntity, int) {
	if kind == "" {
		kind = defaultProjectKind
	}
	type claim struct {
		coordinate string // the name to reach this Entity at
		via        string // the record that said so — the tiebreak key
	}
	byIdentity := map[string]claim{}
	skipped := 0

	for _, r := range records {
		var identity string
		switch r.Type {
		case "A", "AAAA":
			identity = r.Name
		case "CNAME":
			identity = r.Data
		default:
			skipped++
			continue
		}
		if identity == "" || r.Name == "" {
			skipped++
			continue
		}
		// Several records can name one Entity (two aliases, an A and an AAAA). Pick
		// the lexicographically smallest RECORD NAME, deterministically: this runs
		// every poll, and a coordinate that flips between two equally-valid aliases
		// on alternating syncs would make a Run's target depend on when it ran.
		// Within one source's own observations of one Entity, an explicit order is
		// legitimate (the distinction ADR-0143 D2 drew); §2.4's bar is on precedence
		// between SOURCES, which this is not.
		if prev, ok := byIdentity[identity]; ok && prev.via <= r.Name {
			continue
		}
		byIdentity[identity] = claim{coordinate: r.Name, via: r.Name}
	}

	ids := make([]string, 0, len(byIdentity))
	for id := range byIdentity {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic projection order

	out := make([]*pluginv1.ObservedEntity, 0, len(ids))
	for _, id := range ids {
		out = append(out, &pluginv1.ObservedEntity{
			Kind:         kind,
			IdentityKeys: map[string]string{"dns.fqdn": id},
			Facets:       map[string][]byte{"mgmt.address": mgmtAddress(byIdentity[id].coordinate)},
		})
	}
	return out, skipped
}

// defaultProjectKind is what a name in a managed zone denotes when the operator says
// nothing: a host. `dns-record` would be the tidier-looking default and the wrong one —
// it would retype every correlated Entity into a kind no execution View selects.
const defaultProjectKind = "host"

// mgmtAddress renders the closed {address, port?} coordinate (ADR-0084). No port: a DNS
// name carries none, and inventing one would be the core authoring a connection detail
// (§1.4). Hand-rolled rather than marshalled — the shape is two fields and a helper
// that cannot fail keeps the projection total.
func mgmtAddress(name string) []byte {
	return []byte(`{"address":` + quote(name) + `}`)
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// hostPort defaults the DNS port when the operator gave a bare host, so `STRATT_DNS_SERVER=ns1`
// works the way every other endpoint in this repo does.
func hostPort(server string) string {
	if server == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	return net.JoinHostPort(server, "53")
}
