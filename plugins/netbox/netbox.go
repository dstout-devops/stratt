// Package netbox is the NetBox Syncer plugin (ADR-0059): NetBox (netbox-community)
// is an IPAM/DCIM system of record for network topology. This plugin observes its
// REST API and projects `subnet` (from IPAM prefixes) and `vlan` Entities, plus the
// `in-vlan` placement Relation between them, over the sovereign plugin port
// (ADR-0046). The core-side host governs what it may write; the plugin holds no
// graph path.
//
// Charter discipline (ADR-0059): `subnet`/`vlan` are free-string Entity kinds (no
// core change, §1.1). The `net.subnet`/`net.vlan` Facet namespaces are declared
// owned-but-UNCOVERED — the plugin projects a small JSON blob, but ships NO JSON
// Schema (none exists until a Blueprint/Actuator consumes those fields, §1.1 / M1).
// Placement is a typed Relation (`in-vlan`), the topology backbone.
package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Config locates the NetBox Source. The token is resolved from the plugin's own
// broker at spawn (§2.5); material never crosses the core.
type Config struct {
	PluginID string
	Endpoint string // https://netbox.example/  (the API base is <endpoint>/api)
	Token    string // NetBox API token — "Authorization: Token <token>"
	Insecure bool   // dev only
}

// Server implements the sovereign plugin port for a Syncer-class NetBox plugin.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg  Config
	log  *slog.Logger
	http *http.Client
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "netbox"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "netbox"), http: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		// Dual-verb (ADR-0111 D4, on the ADR-0060 Crossplane precedent): a Syncer that OBSERVEs
		// NetBox AND an INVOKE surface providing the `ipam` capability. Class stays the advisory
		// primary kind; Verbs are the authoritative capability surface (the host gates on the verb).
		Class:       pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:       []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		ObserveMode: pluginv1.Manifest_OBSERVE_MODE_POLL,
		// Facet namespaces REQUESTED to own (owned-but-uncovered, ADR-0059 M1 — no
		// JSON Schema until a Contract consumes the fields, §1.1).
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "net.subnet"},
			{SchemaId: "net.vlan"},
		},
		// NetBox ids are the stable per-object identity; tombstone by them on a
		// full sync (ADR-0042).
		TombstoneSchemes: []string{"netbox.prefix.id", "netbox.vlan.id"},
		// `ipam` capability (ADR-0111): NetBox is a global IP/VLAN allocator. Advertised
		// unconditionally — allocation is the plugin's core function and needs only the NetBox
		// endpoint + token a running plugin already has (the honesty argument, ADR-0106 D2). The
		// resolve Action references the CLASS-level, provider-agnostic Contract (ADR-0111 D1).
		Capabilities: []string{"ipam"},
		Actions: []*pluginv1.ActionDecl{{
			Name:       actionIPAMResolve,
			Input:      &pluginv1.ContractRef{SchemaId: "capabilities/ipam.input"},
			Output:     &pluginv1.ContractRef{SchemaId: "capabilities/ipam.output"},
			Idempotent: true, // allocate-or-return-existing, anchored in NetBox (D4/F1)
		}},
	}}, nil
}

// Observe performs a full enumeration of NetBox topology and streams the subnet +
// vlan Entities (with the in-vlan Relation) in one full-sync window.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	entities, err := s.enumerate(stream.Context())
	if err != nil {
		return err
	}
	s.log.Info("netbox enumerated", "endpoint", s.cfg.Endpoint, "entities", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSync: true, FullSyncComplete: true})
}

// ── NetBox REST wire shapes (only the fields we project — §1.1 no speculative typing) ──

type nbList[T any] struct {
	Next    string `json:"next"`
	Results []T    `json:"results"`
}

type nbVLAN struct {
	ID   int    `json:"id"`
	VID  int    `json:"vid"`
	Name string `json:"name"`
}

type nbStatus struct {
	Value string `json:"value"`
}

type nbPrefix struct {
	ID     int      `json:"id"`
	Prefix string   `json:"prefix"` // the CIDR, e.g. "10.0.1.0/24"
	Status nbStatus `json:"status"`
	VLAN   *nbVLAN  `json:"vlan"` // null when the prefix has no VLAN
}

// enumerate is the content-expertise, tested in isolation against a fake NetBox
// (no core, no live server): VLANs → `vlan` Entities; prefixes → `subnet` Entities
// carrying the in-vlan placement Relation when NetBox assigns one.
func (s *Server) enumerate(ctx context.Context) ([]*pluginv1.ObservedEntity, error) {
	out := make([]*pluginv1.ObservedEntity, 0)

	vlans, err := fetchAll[nbVLAN](ctx, s, "/api/ipam/vlans/")
	if err != nil {
		return nil, err
	}
	for _, v := range vlans {
		out = append(out, normalizeVLAN(v))
	}

	prefixes, err := fetchAll[nbPrefix](ctx, s, "/api/ipam/prefixes/")
	if err != nil {
		return nil, err
	}
	for _, p := range prefixes {
		ent, err := normalizePrefix(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ent)
	}
	return out, nil
}

func normalizeVLAN(v nbVLAN) *pluginv1.ObservedEntity {
	facet, _ := json.Marshal(map[string]any{"vid": v.VID, "name": v.Name})
	return &pluginv1.ObservedEntity{
		Kind:         "vlan",
		IdentityKeys: map[string]string{"netbox.vlan.id": strconv.Itoa(v.ID)},
		Labels:       map[string]string{"source": "netbox", "vlan.vid": strconv.Itoa(v.VID)},
		Facets:       map[string][]byte{"net.vlan": facet},
	}
}

func normalizePrefix(p nbPrefix) (*pluginv1.ObservedEntity, error) {
	if strings.TrimSpace(p.Prefix) == "" {
		return nil, fmt.Errorf("netbox: prefix %d has no CIDR; cannot project a subnet without identity", p.ID)
	}
	facet, _ := json.Marshal(map[string]any{"cidr": p.Prefix, "status": p.Status.Value})
	e := &pluginv1.ObservedEntity{
		Kind:         "subnet",
		IdentityKeys: map[string]string{"netbox.prefix.id": strconv.Itoa(p.ID)},
		Labels:       map[string]string{"source": "netbox", "net.cidr": p.Prefix},
		Facets:       map[string][]byte{"net.subnet": facet},
	}
	// Placement (ADR-0059): the subnet sits in-vlan when NetBox assigns one. The
	// target is named BY IDENTITY (netbox.vlan.id); the host resolves it against
	// an already-projected vlan Entity — never a vivified placeholder (ADR-0047).
	if p.VLAN != nil {
		e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
			Type: "in-vlan", ToScheme: "netbox.vlan.id", ToValue: strconv.Itoa(p.VLAN.ID),
		})
	}
	return e, nil
}

// ── lean paginated REST client (net/http; NetBox speaks clean JSON) ──

// fetchAll follows NetBox's `next` pagination, accumulating every result page.
func fetchAll[T any](ctx context.Context, s *Server, path string) ([]T, error) {
	next := strings.TrimRight(s.cfg.Endpoint, "/") + path
	var out []T
	for next != "" {
		page, err := fetchPage[T](ctx, s, next)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Results...)
		next = page.Next // absolute URL from NetBox, or "" when done
		if _, err := url.Parse(next); next != "" && err != nil {
			return nil, fmt.Errorf("netbox: bad next-page url %q: %w", next, err)
		}
	}
	return out, nil
}

func fetchPage[T any](ctx context.Context, s *Server, u string) (nbList[T], error) {
	var zero nbList[T]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return zero, fmt.Errorf("netbox: build request: %w", err)
	}
	if s.cfg.Token != "" {
		req.Header.Set("Authorization", "Token "+s.cfg.Token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("netbox: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return zero, fmt.Errorf("netbox: GET %s: HTTP %d: %s", u, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var list nbList[T]
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return zero, fmt.Errorf("netbox: decode %s: %w", u, err)
	}
	return list, nil
}
