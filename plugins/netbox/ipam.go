// The `ipam` capability provider (ADR-0111): NetBox as a global IP/VLAN allocator. This adds the
// INVOKE verb + a `netbox/ipam-resolve` Action to the (otherwise Syncer-only) netbox plugin — the
// dual-verb shape ADR-0060 blessed for Crossplane. The Action ALLOCATES a prefix from NetBox and
// returns a provider-agnostic `capabilities/ipam.output` handle the core injects into a build
// (never a net.subnet Facet write — the Syncer still OBSERVEs the built subnet, ADR-0111 D3).
//
// Idempotency is anchored IN NETBOX, never in Stratt (ADR-0111 D4/F1): the allocation is stamped
// with the request `key` as the prefix description, and a re-resolve returns the existing prefix.
// Stratt persists no allocation record — NetBox stays the sole allocation system-of-record (§1.2).
package netbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const actionIPAMResolve = "netbox/ipam-resolve"

// descriptionPrefix keys a NetBox prefix's description to the Stratt request identity, so
// allocate-or-return-existing is idempotent and anchored in NetBox (ADR-0111 D4/F1).
const descriptionPrefix = "stratt:ipam:"

// ipamRequest is the decoded capabilities/ipam.input (ADR-0111): allocate a prefix from a pool XOR
// role, scoped by enterprise topology, idempotent on `key`.
type ipamRequest struct {
	Key              string `json:"key"`
	Pool             string `json:"pool"`
	Role             string `json:"role"`
	Size             int    `json:"size"`
	Region           string `json:"region"`
	AvailabilityZone string `json:"availabilityZone"`
	Tenant           string `json:"tenant"`
	VLANGroup        string `json:"vlanGroup"`
}

// resolveIPAM is the ipam capability's resolve Action (ADR-0111): it parses the request, allocates
// (or returns the existing) prefix from NetBox, and emits a capabilities/ipam.output handle.
func (s *Server) resolveIPAM(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest) error {
	var in ipamRequest
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &in); err != nil {
			return status.Errorf(codes.InvalidArgument, "ipam-resolve: invalid args: %v", err)
		}
	}
	if err := in.validate(); err != nil {
		return status.Errorf(codes.InvalidArgument, "%v", err)
	}
	out, err := s.allocateIPAM(stream.Context(), in)
	if err != nil {
		return s.terminalFailure(stream, req, err)
	}
	s.log.Info("ipam resolved", "key", in.Key, "cidr", out["cidr"])
	return s.sendTerminalResult(stream, req, "ipam-resolve "+in.Key, out, "capabilities/ipam.output")
}

func (in ipamRequest) validate() error {
	if in.Key == "" {
		return fmt.Errorf("ipam-resolve requires key (the stable request identity for idempotency)")
	}
	if in.Size <= 0 {
		return fmt.Errorf("ipam-resolve requires a positive size (prefix length)")
	}
	if (in.Pool == "") == (in.Role == "") {
		return fmt.Errorf("ipam-resolve requires exactly one of pool or role")
	}
	return nil
}

// allocateIPAM is the content-expertise, testable in isolation against a fake NetBox (no core, no
// stream): allocate-or-return-existing a prefix keyed on the request, and shape the ipam.output
// handle. Idempotency is anchored in NetBox (ADR-0111 D4/F1).
func (s *Server) allocateIPAM(ctx context.Context, in ipamRequest) (map[string]any, error) {
	desc := descriptionPrefix + in.Key

	// Idempotency: if a prefix already carries our request description, return it — never re-drawn.
	if existing, err := s.findPrefixByDescription(ctx, desc); err != nil {
		return nil, err
	} else if existing != nil {
		return handleFrom(existing), nil
	}

	// Resolve the parent container to carve from (pool = a CIDR/prefix; role = a NetBox role slug),
	// narrowed by tenant scope where given.
	parentID, err := s.resolveParent(ctx, in)
	if err != nil {
		return nil, err
	}

	// Allocate the next free child of the requested length, stamped with the idempotency description.
	body := map[string]any{"prefix_length": in.Size, "description": desc}
	if in.Tenant != "" {
		body["tenant"] = in.Tenant
	}
	var created nbPrefix
	if err := s.postJSON(ctx, fmt.Sprintf("/api/ipam/prefixes/%d/available-prefixes/", parentID), body, &created); err != nil {
		return nil, err
	}
	if created.Prefix == "" {
		return nil, fmt.Errorf("ipam-resolve: NetBox returned an empty prefix for key %q", in.Key)
	}
	return handleFrom(&created), nil
}

// handleFrom shapes a capabilities/ipam.output handle from an allocated NetBox prefix. VLAN
// allocation from a group is a booked follow-on (ADR-0111); a VLAN already bound to the prefix is
// surfaced.
func handleFrom(p *nbPrefix) map[string]any {
	out := map[string]any{"cidr": p.Prefix}
	if p.VLAN != nil {
		out["vlanId"] = p.VLAN.VID
	}
	return out
}

// findPrefixByDescription returns the first NetBox prefix whose description matches (our idempotency
// key), or nil if none — the query-then-allocate lookup that keeps allocation idempotent in NetBox.
func (s *Server) findPrefixByDescription(ctx context.Context, desc string) (*nbPrefix, error) {
	q := url.Values{"description": {desc}}
	list, err := fetchAll[nbPrefix](ctx, s, "/api/ipam/prefixes/?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if len(list) > 0 {
		return &list[0], nil
	}
	return nil, nil
}

// resolveParent finds the NetBox container prefix to carve from: `pool` names a CIDR directly;
// `role` names a NetBox role slug (a container prefix). Tenant narrows the search where given.
func (s *Server) resolveParent(ctx context.Context, in ipamRequest) (int, error) {
	q := url.Values{}
	switch {
	case in.Pool != "":
		q.Set("prefix", in.Pool)
	case in.Role != "":
		q.Set("role", in.Role)
		q.Set("status", "container")
	}
	if in.Tenant != "" {
		q.Set("tenant", in.Tenant)
	}
	list, err := fetchAll[nbPrefix](ctx, s, "/api/ipam/prefixes/?"+q.Encode())
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, fmt.Errorf("ipam-resolve: no parent prefix for pool=%q role=%q (tenant=%q)", in.Pool, in.Role, in.Tenant)
	}
	return list[0].ID, nil
}

// ── Invoke plumbing (this plugin is OBSERVE-only in netbox.go; the INVOKE surface lives here) ──

// Invoke dispatches the netbox plugin's Actions. Today: the ipam capability's resolve Action.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	if req.GetAction() == actionIPAMResolve {
		return s.resolveIPAM(stream, req)
	}
	return status.Errorf(codes.Unimplemented, "netbox: unknown action %q", req.GetAction())
}

// postJSON POSTs a JSON body and decodes the response (NetBox available-prefixes returns the created
// object). 200/201 both mean success.
func (s *Server) postJSON(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("netbox: marshal body: %w", err)
	}
	u := strings.TrimRight(s.cfg.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("netbox: build POST %s: %w", u, err)
	}
	if s.cfg.Token != "" {
		req.Header.Set("Authorization", "Token "+s.cfg.Token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("netbox: POST %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("netbox: POST %s: HTTP %d: %s", u, resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (s *Server) sendTerminalResult(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string, outputs map[string]any, contractID string) error {
	raw, err := json.Marshal(outputs)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("marshal outputs: %w", err))
	}
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, Message: msg,
			At: timestamppb.Now(), CorrelationId: req.GetEnvelope().GetCorrelationId(), Terminal: true, Ok: true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: raw},
			OutputContract: &pluginv1.ContractRef{SchemaId: contractID},
		},
	})
}

func (s *Server) terminalFailure(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, cause error) error {
	s.log.Error("ipam-resolve failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_ERROR, Message: cause.Error(),
		At: timestamppb.Now(), CorrelationId: req.GetEnvelope().GetCorrelationId(), Terminal: true, Ok: false,
	}})
}
