// Package msgraph is the Microsoft Graph Syncer plugin: the Entra directory-device
// delta-query content-expertise that used to live in core/internal/connectors/
// msgraph, now behind the sovereign plugin port (ADR-0046/0047). It maps Graph
// directory devices to core-legible ObservedEntity wire values; the core-side
// host governs what it may write (ownership, identity gating, provenance). The
// plugin holds no graph write path — the host persists the delta cursor.
//
// Transport: the native Graph REST API with delta queries (@odata.deltaLink) —
// the vendor's change-feed transport, §2.2's full-fidelity Syncer path. Auth is
// OAuth2 client credentials. This is the FIRST plugin to use the DELTA-cursor
// Observe path: "" cursor == full initial enumeration; a deltaLink cursor ==
// incremental window carrying @removed as Gone entries.
package msgraph

import (
	"encoding/json"
	"fmt"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// device is the Graph directory-device shape this Syncer consumes — the minimum
// demanded by the Facet namespaces it ships (§1.1: no speculative typing). Delta
// removals arrive as {"id": …, "@removed": {…}}.
type device struct {
	ID                     string `json:"id"` // directory object id (immutable)
	DeviceID               string `json:"deviceId"`
	DisplayName            string `json:"displayName"`
	OperatingSystem        string `json:"operatingSystem"`
	OperatingSystemVersion string `json:"operatingSystemVersion"`
	AccountEnabled         *bool  `json:"accountEnabled"`
	TrustType              string `json:"trustType"`
	ProfileType            string `json:"profileType"`
	ApproxLastSignIn       string `json:"approximateLastSignInDateTime"`
	Removed                *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

// deltaPage is one page of a /devices/delta response.
type deltaPage struct {
	Value     []device `json:"value"`
	NextLink  string   `json:"@odata.nextLink"`
	DeltaLink string   `json:"@odata.deltaLink"`
}

// normalizeDevice maps one observed device onto an ObservedEntity. Pure
// content-expertise — no graph writes (the plugin holds no DB path).
//
// Identity: graph.id (the directory object id) is this Source's own scheme; the
// core-side host decides whether this plugin (by tier + grant) may WRITE it. It
// is NOT a shared cross-source scheme — an Entra object id correlates to nothing
// another Source emits — so it doubles as this Syncer's tombstone scheme.
func normalizeDevice(d device) (*pluginv1.ObservedEntity, error) {
	if d.ID == "" {
		return nil, fmt.Errorf("msgraph: device %q has no object id; cannot project without identity", d.DisplayName)
	}
	identity := map[string]string{"graph.id": d.ID}
	labels := map[string]string{}
	if d.DisplayName != "" {
		labels["graph.name"] = d.DisplayName
	}

	devIdentity := map[string]any{}
	if d.DeviceID != "" {
		devIdentity["azureAdDeviceId"] = d.DeviceID
	}
	if d.TrustType != "" {
		devIdentity["trustType"] = d.TrustType
	}
	if d.ProfileType != "" {
		devIdentity["profileType"] = d.ProfileType
	}
	devOS := map[string]any{}
	if d.OperatingSystem != "" {
		devOS["operatingSystem"] = d.OperatingSystem
	}
	if d.OperatingSystemVersion != "" {
		devOS["version"] = d.OperatingSystemVersion
	}
	devState := map[string]any{}
	if d.AccountEnabled != nil {
		devState["accountEnabled"] = *d.AccountEnabled
	}
	if d.ApproxLastSignIn != "" {
		devState["approximateLastSignInDateTime"] = d.ApproxLastSignIn
	}

	facets := map[string][]byte{}
	for ns, doc := range map[string]map[string]any{
		"device.identity": devIdentity,
		"device.os":       devOS,
		"device.state":    devState,
	} {
		if len(doc) == 0 {
			continue
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("msgraph: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return &pluginv1.ObservedEntity{
		Kind:         "device",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, nil
}
