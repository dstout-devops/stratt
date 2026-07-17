// Package staticinv is the static-inventory Syncer plugin (ADR-0056 §5): a
// Connector whose system-of-record is a host-list file in the estate repo —
// "devices as code". It projects each declared host as a `host` Entity over the
// sovereign plugin port (ADR-0046); the core-side host performs the WriterSyncer
// graph write (§1.2). The FILE is authoritative — Stratt projects it and never
// writes back — so the graph stays a rebuildable projection, not a writable CMDB
// (a permanent non-goal).
//
// It declares NO facet Contract and NO tombstone scheme, deliberately:
//   - No facet (§1.1 — type the seams, not the world): it projects existence +
//     dns.fqdn identity + operator labels ONLY. A host's fileset/os.kernel/access
//     Facets are OBSERVED by the collectors (the ansible gather Runs), a DIFFERENT
//     write-owner — projecting them here would be a §2.1 owner conflict.
//   - No tombstone scheme (ADR-0056 §5): dropping a host from the file must NOT
//     silently delete its Entity. With no granted tombstone scheme the core host
//     tombstones nothing on the full-sync boundary, so a removed host lingers
//     until a deliberate decommission — never a reconcile delete (§2.4). The
//     max-delta-gated orphan Finding is the documented follow-up.
package staticinv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Config locates the static-inventory Source: a directory of host-list files.
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on
	Path     string // directory holding the host-list files (*.yaml)
}

// Server implements the sovereign plugin port for a Syncer-class static-inventory
// plugin. It holds no graph write path; it maps the file to core-legible
// ObservedEntity wire values and the core-side host governs the write.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "staticinv"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "staticinv")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE},
		ObserveMode:     pluginv1.Manifest_OBSERVE_MODE_POLL,
		// Contracts: none — projects identity + labels only, no facet (§1.1).
		// TombstoneSchemes: none — a removed host is never silently deleted (§5).
	}}, nil
}

// Observe performs a full enumeration of the host-list directory each poll and
// streams the hosts as ObservedEntities. FullSyncComplete is honest (the whole
// SoR was read); it is safe because the manifest grants no tombstone scheme, so
// the core host tombstones nothing (see core internal/pluginhost host.go).
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	entities, err := enumerate(s.cfg.Path)
	if err != nil {
		return err
	}
	s.log.Info("static inventory enumerated", "path", s.cfg.Path, "hosts", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{
		Entities:         entities,
		FullSync:         true,
		FullSyncComplete: true,
	})
}

// hostFile is the static-inventory boundary Contract (ADR-0056 §5): the plugin's
// OWN minimal file format, strict-decoded — NOT a universal host ontology (§1.1).
type hostFile struct {
	Hosts []hostEntry `yaml:"hosts"`
}

type hostEntry struct {
	FQDN   string            `yaml:"fqdn"`
	Labels map[string]string `yaml:"labels"`
}

// enumerate reads every *.yaml host-list file under dir and maps each declared
// host to an ObservedEntity. It is the content-expertise, tested in isolation (no
// gRPC, no core) — the server just streams what this returns.
func enumerate(dir string) ([]*pluginv1.ObservedEntity, error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("staticinv: read inventory dir %s: %w", dir, err)
	}
	var files []string
	for _, e := range dirents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files) // deterministic projection order

	out := make([]*pluginv1.ObservedEntity, 0)
	seen := map[string]string{} // dns.fqdn -> file that declared it (identity-collision guard)
	for _, name := range files {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("staticinv: read %s: %w", path, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true) // reject unknown keys — the file is a pinned boundary, not free-form
		var hf hostFile
		if err := dec.Decode(&hf); err != nil {
			return nil, fmt.Errorf("staticinv: %s: %w", name, err)
		}
		for i, h := range hf.Hosts {
			ent, err := normalize(h)
			if err != nil {
				return nil, fmt.Errorf("staticinv: %s host %d: %w", name, i, err)
			}
			fqdn := ent.GetIdentityKeys()["dns.fqdn"]
			if prev, dup := seen[fqdn]; dup {
				return nil, fmt.Errorf("staticinv: duplicate host %q (declared in %s and %s); dns.fqdn is the identity key", fqdn, prev, name)
			}
			seen[fqdn] = name
			out = append(out, ent)
		}
	}
	return out, nil
}

// normalize maps one declared host to an ObservedEntity: kind `host`, dns.fqdn
// identity, and the operator's labels (per-key owned, ADR-0041). No facet.
func normalize(h hostEntry) (*pluginv1.ObservedEntity, error) {
	fqdn := strings.ToLower(strings.TrimSpace(h.FQDN))
	if fqdn == "" {
		return nil, fmt.Errorf("host has no fqdn; dns.fqdn is the required identity key (§1.2 — no identity, no projection)")
	}
	if strings.ContainsAny(fqdn, " \t\r\n") {
		return nil, fmt.Errorf("fqdn %q contains whitespace", h.FQDN)
	}
	labels := make(map[string]string, len(h.Labels))
	for k, v := range h.Labels {
		labels[k] = v
	}
	return &pluginv1.ObservedEntity{
		Kind:         "host",
		IdentityKeys: map[string]string{"dns.fqdn": fqdn},
		Labels:       labels,
	}, nil
}
