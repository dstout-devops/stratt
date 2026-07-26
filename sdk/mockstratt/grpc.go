package mockstratt

import (
	"context"
	"fmt"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// The gRPC transport: a long-lived plugin server the core dials (invariant #4).
// The core is the CLIENT here — which is why this file has no server in it. A
// plugin author who wants a server writes one; this is the thing that connects to
// it, negotiates, governs, and refuses.

// Conn is a live connection to a plugin serving the sovereign port.
type Conn struct {
	client pluginv1.PluginServiceClient
	closer func() error
}

// Dial connects to a plugin's gRPC endpoint over an insecure channel.
//
// INSECURE IS A DEVELOPMENT-ONLY CHOICE AND IT MATTERS. In production the channel
// is authenticated (mTLS / per-plugin token) and that is the ONLY reason a plugin
// may trust Envelope.principal at all (invariant #3). A plugin whose trust
// decisions would change if the channel were unauthenticated has a bug this
// harness cannot show it — so treat a pass here as saying nothing about identity.
func Dial(ctx context.Context, target string) (*Conn, error) {
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("mockstratt: dial %s: %w", target, err)
	}
	return &Conn{client: pluginv1.NewPluginServiceClient(cc), closer: cc.Close}, nil
}

// NewConn wraps an existing client — the seam for an in-process plugin under test
// (a bufconn listener, or a direct struct implementing the service). This is the
// fastest path to a plugin test that never opens a socket, and it exercises the
// same governor.
func NewConn(client pluginv1.PluginServiceClient) *Conn {
	return &Conn{client: client, closer: func() error { return nil }}
}

// Close releases the connection.
func (c *Conn) Close() error { return c.closer() }

// Client exposes the raw port client for verbs this harness does not govern
// (WrapKey, Subscribe). Reaching for it is fine; note only that what comes back is
// UNGOVERNED — the core would still gate it.
func (c *Conn) Client() pluginv1.PluginServiceClient { return c.client }

// Register performs the core's registration handshake: fetch the Manifest and
// check it against the operator grant.
//
// THE MANIFEST IS A REQUEST, NOT A GRANT. Everything a plugin advertises is
// checked against what the operator declared, and anything outside it fails
// registration outright rather than being trimmed — because a plugin that syncs
// with silently-reduced authority looks healthy while writing nothing (§1.8), and
// because a self-granting plugin would let connection order decide ownership
// (§2.4). This is the check plugin authors are most surprised by, so it is the
// first thing this harness does.
func (c *Conn) Register(ctx context.Context, grant Grant) (*pluginv1.Manifest, error) {
	resp, err := c.client.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return nil, fmt.Errorf("mockstratt: GetManifest: %w", err)
	}
	m := resp.GetManifest()
	if m == nil {
		return nil, fmt.Errorf("mockstratt: GetManifest returned no manifest")
	}
	// Anti-spoof: plugin_id is a string the plugin controls, so it is CHECKED
	// against the authenticated channel identity, never trusted (ADR-0049 F1).
	if m.GetPluginId() != grant.PluginIdentity {
		return nil, fmt.Errorf("mockstratt: manifest plugin_id %q does not match the grant identity %q",
			m.GetPluginId(), grant.PluginIdentity)
	}
	for _, cd := range m.GetContracts() {
		if !grant.allowsFacet(cd.GetSchemaId()) {
			return nil, fmt.Errorf("mockstratt: manifest requests ownership of %q, which the operator grant does not include",
				cd.GetSchemaId())
		}
	}
	for _, s := range m.GetTombstoneSchemes() {
		if ok, reason := grant.allowsIdentity(s); !ok {
			return nil, fmt.Errorf("mockstratt: manifest requests tombstone scheme %q: %s", s, reason)
		}
	}
	return m, nil
}

// Apply calls the plugin's Apply and returns the GOVERNED result — the same
// verdict the subprocess transport produces, from the same governor.
func (c *Conn) Apply(ctx context.Context, host *Host, req Request) (Result, error) {
	stream, err := c.client.Apply(ctx, req.ApplyRequest())
	if err != nil {
		return Result{}, fmt.Errorf("mockstratt: Apply: %w", err)
	}
	return host.Govern(ctx, stream, req.Targets)
}

// Plan calls the plugin's Plan verb and returns its domain diff.
//
// The diff is returned OPAQUE, and that is not laziness: the core stores it and
// can descend into it (§1.8) but never interprets it. A harness that decoded it
// would be teaching plugin authors that the core reads their payloads.
func (c *Conn) Plan(ctx context.Context, req Request) (*pluginv1.PlanResponse, error) {
	resp, err := c.client.Plan(ctx, &pluginv1.PlanRequest{
		Envelope:             req.ApplyRequest().GetEnvelope(),
		Desired:              &pluginv1.Payload{Bytes: req.Params},
		ResolvedCapabilities: req.Capabilities,
	})
	if err != nil {
		return nil, fmt.Errorf("mockstratt: Plan: %w", err)
	}
	return resp, nil
}

// ObserveResult is a governed Syncer window set — the projection proposals that
// survived the gates, plus every refusal.
type ObserveResult struct {
	Entities         []Entity
	Gone             []GoneEntity
	FullSyncComplete bool
	NextCursor       string
	Rejections       []Rejection
}

// GoneEntity is a delta 'leave' — an Entity the Source no longer reports, by the
// identity scheme it tombstones on (ADR-0042).
type GoneEntity struct {
	Scheme string
	Value  string
}

// Observe drains the plugin's Observe stream and governs it under the same
// tier+grant gates as Apply write-back.
//
// Tombstoning is where a Syncer is most dangerous — liveness crosses the wire, so
// a full sync that under-reports DELETES estate. The gates below are the reason a
// plugin author should run this before pointing anything at a real Source.
func (c *Conn) Observe(ctx context.Context, host *Host, cursor string) (ObserveResult, error) {
	stream, err := c.client.Observe(ctx, &pluginv1.ObserveRequest{Cursor: cursor})
	if err != nil {
		return ObserveResult{}, fmt.Errorf("mockstratt: Observe: %w", err)
	}
	out := ObserveResult{}
	reject := func(kind, detail, reason string) {
		out.Rejections = append(out.Rejections, Rejection{Kind: kind, Detail: detail, Reason: reason})
	}
	for {
		resp, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("mockstratt: observe recv: %w", rerr)
		}
		for _, e := range resp.GetEntities() {
			// grantOnly, not stepScoped: a Syncer has no Step and therefore no
			// per-Run write-scope floor — its ownership comes from the grant alone.
			if ent, ok := host.governEntity(e, grantOnly, "observe", reject); ok {
				out.Entities = append(out.Entities, ent)
			}
		}
		for _, g := range resp.GetGone() {
			if ok, reason := host.grant.allowsIdentity(g.GetScheme()); !ok {
				reject("identity-scheme", g.GetScheme(), "observe: tombstone by ungranted scheme: "+reason)
				continue
			}
			out.Gone = append(out.Gone, GoneEntity{Scheme: g.GetScheme(), Value: g.GetValue()})
		}
		if resp.GetFullSyncComplete() {
			out.FullSyncComplete = true
		}
		if nc := resp.GetNextCursor(); nc != "" {
			out.NextCursor = nc
		}
	}
	return out, nil
}
