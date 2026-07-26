package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// maxJumpHops bounds a reached-via chain. A bound exists at all because the chain is
// graph data: a cycle is caught explicitly below, but an adversarially long legitimate
// chain would still be a slow per-target walk on every dispatch. Eight is far past any
// real topology (a fleet behind two bastions is already unusual) and matches the
// contract's `connection.jump` maxItems, so the two limits cannot disagree.
const maxJumpHops = 8

// resolveJumpChain walks the reached-via Relations from an Entity and returns the
// bastion chain, NEAREST HOP FIRST (ADR-0126 D3).
//
// Each hop's coordinate is read from that hop's OWN mgmt.address Facet — never copied
// onto the target — so a bastion's address has exactly one home and cannot drift from
// what the graph says (§2.4). This is the whole reason the jump path is a Relation
// rather than a field: a string would duplicate a fact the graph already holds, and
// ADR-0084 D1 closed the mgmt.address schema precisely so it could not grow one (§9).
//
// Two failures are LOUD rather than silent, and both for the same reason: a target
// declared to be behind a bastion that is then reached directly is worse than a target
// that fails to be reached at all (§1.8, matching ADR-0084 D2's no-silent-localhost
// rule).
//   - a cycle — a chain that never terminates
//   - a hop with no mgmt.address — a bastion nothing knows how to reach
func resolveJumpChain(ctx context.Context, store relationReader, entityID string) ([]actuators.Hop, error) {
	var chain []actuators.Hop
	seen := map[string]bool{entityID: true}
	cur := entityID

	for len(chain) <= maxJumpHops {
		next, err := store.RelationTargets(ctx, cur, types.RelReachedVia)
		if err != nil {
			return nil, err
		}
		switch len(next) {
		case 0:
			return chain, nil
		case 1:
		default:
			// Two bastions for one host is not a routing choice the core may make:
			// picking one would be exactly the implicit precedence §2.4 forbids, and
			// there is no field to break the tie because there must not be one.
			return nil, fmt.Errorf("entity %s declares %d reached-via hops — a host is reached through ONE bastion; a second is an ambiguity core will not resolve (§2.4)", cur, len(next))
		}
		hop := next[0]
		if seen[hop] {
			return nil, fmt.Errorf("reached-via chain from %s cycles at %s", entityID, hop)
		}
		seen[hop] = true

		ent, err := store.GetEntity(ctx, hop)
		if err != nil {
			return nil, fmt.Errorf("reached-via hop %s of %s: %w", hop, entityID, err)
		}
		raw, err := store.FacetValuesByEntities(ctx, "mgmt.address", []string{hop})
		if err != nil {
			return nil, err
		}
		addr, port := addressOf(raw[hop])
		if addr == "" {
			return nil, fmt.Errorf("reached-via hop %s of %s carries no mgmt.address — a bastion nothing can reach is not a route, and connecting directly instead would silently ignore the declared path (§1.8)", hop, entityID)
		}
		chain = append(chain, actuators.Hop{Name: observedName(ent), Address: addr, Port: port})
		cur = hop
	}
	return nil, fmt.Errorf("reached-via chain from %s exceeds %d hops", entityID, maxJumpHops)
}

// relationReader is the slice of the graph store a chain walk needs. Narrow on purpose:
// the walk reads relations, entities and one Facet, and cannot write anything (§1.2).
type relationReader interface {
	RelationTargets(ctx context.Context, fromID, relType string) ([]string, error)
	GetEntity(ctx context.Context, id string) (types.Entity, error)
	FacetValuesByEntities(ctx context.Context, ns string, ids []string) (map[string]json.RawMessage, error)
}

// portHops / protoHops carry a resolved chain onto the two wire shapes. Both exist
// because BOTH target-building paths must carry it: a chain that reached the gRPC
// Actuator but not the EE-Job one would be an asymmetry nobody notices until a
// bastion'd fleet is dispatched to the wrong one (the sibling-path defect ADR-0118's
// four launch doors and ADR-0125's two dispatch paths each had to close).
func portHops(hops []actuators.Hop) []pluginhost.JumpHop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]pluginhost.JumpHop, 0, len(hops))
	for _, h := range hops {
		out = append(out, pluginhost.JumpHop{Name: h.Name, Address: h.Address, Port: h.Port})
	}
	return out
}

func protoHops(hops []actuators.Hop) []*pluginv1.JumpHop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]*pluginv1.JumpHop, 0, len(hops))
	for _, h := range hops {
		out = append(out, &pluginv1.JumpHop{Name: h.Name, Address: h.Address, Port: h.Port})
	}
	return out
}
