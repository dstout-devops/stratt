package msgraph

import (
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// device is the Graph directory-device shape this Syncer consumes — the
// minimum demanded by the Facet namespaces it ships (§1.1: no speculative
// typing). Delta removals arrive as {"id": …, "@removed": {…}}.
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

// normalizeDevice maps one observed device onto the graph shape. Pure
// function — all writes go through the Projector in the Syncer.
func normalizeDevice(d device) (up graph.EntityUpsert, err error) {
	if d.ID == "" {
		return up, fmt.Errorf("msgraph: device %q has no object id; cannot project without identity", d.DisplayName)
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

	facets := map[string]json.RawMessage{}
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
			return up, fmt.Errorf("msgraph: marshal facet %s: %w", ns, err)
		}
		facets[ns] = raw
	}

	return graph.EntityUpsert{
		Kind:         "device",
		IdentityKeys: identity,
		Labels:       labels,
		Facets:       facets,
	}, nil
}
