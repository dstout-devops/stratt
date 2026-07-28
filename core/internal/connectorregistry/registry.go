// Package connectorregistry is the runtime registry that reconciles the declared Connector
// and Actuator sets (CaC desired state, ADR-0103) against a live set of dialed + registered
// plugins — enabling/disabling each with NO strattd restart. It is the "make it so" half of
// the two desired-state Kinds (the desired-state engine writes graph.connector/graph.actuator;
// this reconciles those rows into running plugins), modeled on triggers.Reconciler.
//
// Two reconcile loops with a deliberate replica/leader split (ADR-0103 D3):
//   - Actuators (+ Connector Action capability) do NO graph writes → their Actuator/Action
//     dispatch-map membership reconciles on EVERY replica (idempotent), so an activity on any
//     worker finds them.
//   - Connector Syncers ARE graph writers → they reconcile LEADER-ONLY, dialed + Registered +
//     run under homegate.Supervise (single-writer, home-gated).
//
// Enable failures and §2.4 name collisions are REJECTED + SURFACED (never a silent log): each
// declaration carries a queryable runtime Status (D6, §1.8).
package connectorregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/homegate"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/planstore"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// Dialer opens a gRPC connection to a plugin endpoint (grpc.NewClient in production).
type Dialer func(addr string) (*grpc.ClientConn, error)

// AdvertisedAction is one ActionDecl as core reads it: the dispatch-name selector plus the
// Contract IDs the plugin says it conformance-checks that Action's args/outputs against.
//
// Core keeps the ids OPAQUE — it never reads the plugin's schema to interpret content (§1.5,
// port invariant #5). It only checks that an id NAMES a document core also holds, because a
// Contract core cannot resolve is one it cannot validate a Step against; the plugin would be
// checking args against a document core has never seen.
type AdvertisedAction struct {
	Name           string
	InputContract  string
	OutputContract string
	// Implements is the capability class this Action IS, or "" for an ordinary Action
	// (ADR-0140 D1). Carried opaquely — core never derives it and never parses the name.
	Implements string
	// InputSha / OutputSha are the CONTENT HASHES the plugin pins its refs to (port invariant
	// #5). Empty means unpinned — which is lawful for a SEAM the plugin does not own (a
	// capability class contract, or a neutrally-named one like cert-issuer's), because a plugin
	// cannot hash a document it does not ship. A pinned ref that DISAGREES with core's copy is
	// drift, and drift is blocking.
	InputSha  string
	OutputSha string
}

// PluginManifest is the core-side view of a plugin's Manifest — the §1.5 VERIFICATION input,
// distinct from the operator's declaration. Injectable so tests need no live plugin.
//
// It carries only what core acts on. `Capabilities` verifies declared `provides` (ADR-0104 D1);
// `Actions` verifies declared `actionNames` (below). Widening this struct is how a new
// advertisement reaches core — the fetcher signature stops being the bottleneck it was when it
// returned a bare []string and discarded everything else the plugin said.
type PluginManifest struct {
	Capabilities []string
	Actions      []AdvertisedAction
}

// ManifestFetcher fetches a plugin's advertised Manifest from addr.
type ManifestFetcher func(ctx context.Context, addr string) (PluginManifest, error)

// Status is the per-declaration runtime enable state (ADR-0103 D6): observable so a declared
// Connector/Actuator that silently isn't running shows WHY (dial error, §2.4 collision, …).
type Status struct {
	Enabled bool   `json:"enabled"`
	Error   string `json:"error,omitempty"`
}

type entry struct {
	conn        *grpc.ClientConn   // nil for an EE-Job actuator (no dial)
	cancel      context.CancelFunc // syncer supervise-loop cancel; nil for actuators
	host        *pluginhost.Host
	actuatorKey string   // registered actuator dispatch name (for teardown)
	actionNames []string // registered action dispatch names (for teardown)
	specJSON    string   // change detection → update = disable+enable
}

// Registry owns the live plugin set and reconciles it against the declared rows.
type Registry struct {
	store    *graph.Store
	plugins  *orchestrate.PluginRegistry
	homeDeps homegate.Deps
	plans    *planstore.Store
	dial     Dialer
	manifest ManifestFetcher
	interval time.Duration
	log      *slog.Logger

	mu          sync.Mutex // guards the entry maps
	actEntries  map[string]*entry
	connEntries map[string]*entry

	smu    sync.Mutex // guards status (separate lock: enable/disable hold mu and set status)
	status map[string]Status
}

// New builds a registry. interval is the reconcile cadence (default 30s).
func New(store *graph.Store, plugins *orchestrate.PluginRegistry, homeDeps homegate.Deps, plans *planstore.Store, dial Dialer, interval time.Duration, log *slog.Logger) *Registry {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Registry{
		store: store, plugins: plugins, homeDeps: homeDeps, plans: plans, dial: dial, interval: interval, log: log,
		actEntries: map[string]*entry{}, connEntries: map[string]*entry{}, status: map[string]Status{},
	}
	r.manifest = r.dialManifest // default: dial the plugin + GetManifest; tests inject a fake
	return r
}

// dialManifest is the production ManifestFetcher: a short-lived dial to the plugin's
// sovereign-port endpoint that reads its advertised capabilities and closes the connection.
func (r *Registry) dialManifest(ctx context.Context, addr string) (PluginManifest, error) {
	conn, err := r.dial(addr)
	if err != nil {
		return PluginManifest{}, err
	}
	defer conn.Close()
	resp, err := pluginv1.NewPluginServiceClient(conn).GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return PluginManifest{}, err
	}
	m := resp.GetManifest()
	out := PluginManifest{Capabilities: m.GetCapabilities()}
	for _, a := range m.GetActions() {
		out.Actions = append(out.Actions, AdvertisedAction{
			Name:           a.GetName(),
			InputContract:  a.GetInput().GetSchemaId(),
			OutputContract: a.GetOutput().GetSchemaId(),
			InputSha:       a.GetInput().GetSha256(),
			OutputSha:      a.GetOutput().GetSha256(),
			Implements:     a.GetImplements(),
		})
	}
	return out, nil
}

// ── status (D6) ─────────────────────────────────────────────────────────────

func (r *Registry) setStatus(key string, enabled bool, errMsg string) {
	r.smu.Lock()
	defer r.smu.Unlock()
	r.status[key] = Status{Enabled: enabled, Error: errMsg}
}

func (r *Registry) clearStatus(key string) {
	r.smu.Lock()
	defer r.smu.Unlock()
	delete(r.status, key)
}

// Statuses returns a snapshot of every declaration's runtime status, keyed "<kind>/<name>".
func (r *Registry) Statuses() map[string]Status {
	r.smu.Lock()
	defer r.smu.Unlock()
	out := make(map[string]Status, len(r.status))
	for k, v := range r.status {
		out[k] = v
	}
	return out
}

// Status returns one declaration's runtime status (kind ∈ {"connector","actuator"}).
func (r *Registry) Status(kind, name string) (Status, bool) {
	r.smu.Lock()
	defer r.smu.Unlock()
	s, ok := r.status[kind+"/"+name]
	return s, ok
}

// ── Actuator reconcile (every replica) ──────────────────────────────────────

// RunActuators is the every-replica reconcile loop (ADR-0103 D3). NOT leader-gated.
func (r *Registry) RunActuators(ctx context.Context) {
	for {
		r.ReconcileActuators(ctx)
		select {
		case <-time.After(r.interval):
		case <-ctx.Done():
			return
		}
	}
}

// ReconcileActuators dials+registers newly-declared Actuators into the dispatch table and
// drops undeclared ones — on every replica (Actuators do no graph writes).
func (r *Registry) ReconcileActuators(ctx context.Context) {
	decls, err := r.store.ListActuators(ctx)
	if err != nil {
		r.log.Warn("connectorregistry: list actuators", "err", err)
		return
	}
	res, err := r.buildProviderIndex(ctx)
	if err != nil {
		r.log.Warn("connectorregistry: build provider index", "err", err)
		return
	}
	declared := make(map[string]types.Actuator, len(decls))
	for _, a := range decls {
		declared[a.Name] = a
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, e := range r.actEntries {
		if _, ok := declared[name]; !ok {
			r.disableActuatorLocked(name, e)
		}
	}
	for name, a := range declared {
		spec := mustJSON(a)
		if e, ok := r.actEntries[name]; ok {
			if e.specJSON == spec {
				continue // unchanged
			}
			r.disableActuatorLocked(name, e) // spec changed → re-enable fresh
		}
		r.enableActuatorLocked(ctx, a, spec, res)
	}
}

func (r *Registry) enableActuatorLocked(ctx context.Context, a types.Actuator, spec string, res resolution) {
	key := "actuator/" + a.Name
	// Capability dependency gate (ADR-0104 D3): withhold the Actuator from the dispatch table
	// while any required capability is unmet/ambiguous — surfaced as a PENDING D6 status, not a
	// crash. Gating happens only here (at enable), never a cascade-disable of an already-running
	// Actuator when a provider later disappears (D5 — provider outages are diagnosed per-Run).
	if ok, reason := classifyRequires(a.Requires, res); !ok {
		r.setStatus(key, false, reason)
		r.log.Info("connector registry: actuator pending on dependency", "name", a.Name, "reason", reason)
		return
	}
	grant := actuatorGrant(a)
	var conn *grpc.ClientConn
	var client pluginv1.PluginServiceClient
	if len(a.JobCommand) == 0 { // gRPC transport; an EE-Job actuator has no dial
		c, err := r.dial(a.Address)
		if err != nil {
			r.setStatus(key, false, "dial "+a.Address+": "+err.Error())
			r.log.Warn("connectorregistry: actuator dial failed", "name", a.Name, "err", err)
			return
		}
		conn = c
		client = pluginv1.NewPluginServiceClient(c)
		if err := r.verifyDeclaredActions(ctx, a.Address, a.ActionNames); err != nil {
			conn.Close()
			r.setStatus(key, false, err.Error())
			r.log.Warn("connectorregistry: actuator actionNames rejected", "name", a.Name, "err", err)
			return
		}
	} else if len(a.ActionNames) > 0 {
		// An EE-Job Actuator has no dial address, so its advertisement cannot be fetched —
		// the same dial-less verification gap ADR-0138 D5 books for `provides`. No shipped
		// declaration is in this shape; when one is, it registers UNVERIFIED rather than
		// silently appearing checked.
		r.log.Warn("connector registry: actionNames not verifiable on an EE-Job Actuator (no dial address, ADR-0138 D5)",
			"name", a.Name, "actions", a.ActionNames)
	}
	host := pluginhost.New(r.store, client, grant, r.log)
	if r.plans != nil {
		host = host.UsePlanStore(r.plans)
	}
	pa := orchestrate.PluginActuator{
		Host: host, DryRunnable: a.DryRunnable, Grant: grant, PlanStore: r.plans,
		JobCommand: a.JobCommand, Image: a.Image, MCP: a.MCP, Requires: a.Requires,
		// The declared tool-content tree, already resolved at estate-parse time (ADR-0134
		// D3) and carried through the stored spec — so it reaches every replica by the same
		// route the rest of the declaration does, and dispatch never reads a filesystem.
		Content: a.Content,
	}
	if err := r.plugins.RegisterActuator(a.Name, pa); err != nil {
		// §2.4 collision → reject + surface (D4/D6), never crash the daemon.
		r.setStatus(key, false, err.Error())
		if conn != nil {
			conn.Close()
		}
		r.log.Warn("connectorregistry: actuator register rejected", "name", a.Name, "err", err)
		return
	}
	e := &entry{conn: conn, host: host, actuatorKey: a.Name, specJSON: spec}
	for _, an := range a.ActionNames {
		// Requires rides from the SAME declaration the Actuator got it from (ADR-0145 D2).
		// One declaration, one `requires:`, both its dispatch surfaces — otherwise a build
		// served as an Action silently runs without the statestore/ipam its sibling Apply
		// is guaranteed.
		if err := r.plugins.RegisterAction(an, orchestrate.PluginAction{Host: host, DryRunnable: a.DryRunnable, Requires: a.Requires}); err != nil {
			r.setStatus(key, false, "action "+an+": "+err.Error())
			r.log.Warn("connectorregistry: action register rejected", "name", a.Name, "action", an, "err", err)
			continue // keep the actuator; the action collided
		}
		e.actionNames = append(e.actionNames, an)
	}
	r.actEntries[a.Name] = e
	r.setStatus(key, true, "")
	r.log.Info("connector registry: actuator enabled", "name", a.Name, "actions", e.actionNames)
}

func (r *Registry) disableActuatorLocked(name string, e *entry) {
	r.plugins.DeregisterActuator(e.actuatorKey)
	for _, an := range e.actionNames {
		r.plugins.DeregisterAction(an)
	}
	if e.conn != nil {
		e.conn.Close()
	}
	delete(r.actEntries, name)
	r.clearStatus("actuator/" + name)
	r.log.Info("connector registry: actuator disabled", "name", name)
}

// ── Connector (Syncer) reconcile (leader only) ──────────────────────────────

// RunConnectors is the leader-only reconcile loop for Connector Syncers (ADR-0103 D3).
// Appended to the leader-gated controllers in strattd.
func (r *Registry) RunConnectors(ctx context.Context) {
	for {
		r.ReconcileConnectors(ctx)
		select {
		case <-time.After(r.interval):
		case <-ctx.Done():
			// Cancel every supervised loop as the leader loop tears down.
			r.mu.Lock()
			for name, e := range r.connEntries {
				r.disableConnectorLocked(context.Background(), name, e)
			}
			r.mu.Unlock()
			return
		}
	}
}

// ReconcileConnectors dials+Registers newly-declared Connector Syncers under home-gated
// supervision and tears down undeclared ones. Class "action" Connectors are not yet wired
// (ADR-0103 slice-1 is syncer-only) — surfaced via Status, never silently dropped.
func (r *Registry) ReconcileConnectors(ctx context.Context) {
	decls, err := r.store.ListConnectors(ctx)
	if err != nil {
		r.log.Warn("connectorregistry: list connectors", "err", err)
		return
	}
	res, err := r.buildProviderIndex(ctx)
	if err != nil {
		r.log.Warn("connectorregistry: build provider index", "err", err)
		return
	}
	declared := make(map[string]types.Connector, len(decls))
	for _, c := range decls {
		// `syncer` and `action` are both wired now. An EMITTER is still reserved (ADR-0103): it
		// pushes events rather than being pulled or invoked, so it needs an ingest path this
		// registry does not have — refusing it is honest, and it says so.
		if c.Class != types.ConnectorSyncer && c.Class != types.ConnectorAction {
			r.setStatus("connector/"+c.Name, false, "class "+c.Class+" not yet wired (ADR-0103: syncer + action)")
			continue
		}
		declared[c.Name] = c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, e := range r.connEntries {
		if _, ok := declared[name]; !ok {
			r.disableConnectorLocked(ctx, name, e)
		}
	}
	for name, c := range declared {
		spec := mustJSON(c)
		if e, ok := r.connEntries[name]; ok {
			if e.specJSON == spec {
				continue
			}
			r.disableConnectorLocked(ctx, name, e)
		}
		r.enableConnectorLocked(ctx, c, spec, res)
	}
}

func (r *Registry) enableConnectorLocked(ctx context.Context, c types.Connector, spec string, res resolution) {
	key := "connector/" + c.Name
	// Capability dependency gate (ADR-0104 D3): hold the Syncer PENDING (unregistered, unhomed)
	// while any required capability is unmet/ambiguous — observable via D6, never a crash (§1.8).
	if ok, reason := classifyRequires(c.Requires, res); !ok {
		r.setStatus(key, false, reason)
		r.log.Info("connector registry: connector pending on dependency", "name", c.Name, "reason", reason)
		return
	}
	conn, err := r.dial(c.Address)
	if err != nil {
		r.setStatus(key, false, "dial "+c.Address+": "+err.Error())
		r.log.Warn("connectorregistry: connector dial failed", "name", c.Name, "err", err)
		return
	}
	if aerr := r.verifyDeclaredActions(ctx, c.Address, c.ActionNames); aerr != nil {
		conn.Close()
		r.setStatus(key, false, aerr.Error())
		r.log.Warn("connectorregistry: connector actionNames rejected", "name", c.Name, "err", aerr)
		return
	}
	host := pluginhost.New(r.store, pluginv1.NewPluginServiceClient(conn), connectorGrant(c), r.log)
	e := &entry{conn: conn, host: host, specJSON: spec}

	// A Connector's Actions reach the DISPATCH TABLE (§2.2, ADR-0031). Until now they were
	// verified against the Manifest and then registered nowhere: `actionNames` on a Connector
	// was admitted, checked, and silently unusable — a Step naming one failed at dispatch with
	// "no action registered", which is the half-declaration defect this package refuses
	// everywhere else. The Actuator path has always done this; the Connector path had the
	// verification without the registration.
	for _, an := range c.ActionNames {
		if err := r.plugins.RegisterAction(an, orchestrate.PluginAction{Host: host, Requires: c.Requires}); err != nil {
			// §2.4 collision → reject + surface, never a crash (D4/D6). Roll back what this
			// enable already registered so a partial dispatch surface never survives the failure.
			for _, done := range e.actionNames {
				r.plugins.DeregisterAction(done)
			}
			conn.Close()
			r.setStatus(key, false, "action "+an+": "+err.Error())
			r.log.Warn("connectorregistry: connector action register rejected", "name", c.Name, "action", an, "err", err)
			return
		}
		e.actionNames = append(e.actionNames, an)
	}

	// The SYNCER half is syncer-class only. An `action` Connector observes nothing: it must not
	// claim a Source, must not run an Observe loop, and must not be home-gated — the home gate
	// exists to keep ONE writer per Source, and a Connector that writes nothing has no Source to
	// contend for. Running it anyway would register a projection owner that never projects.
	if c.Class == types.ConnectorSyncer {
		interval := time.Duration(c.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 30 * time.Second
		}
		// Home-gated supervised Syncer under a per-connector child context (single-writer,
		// ADR-0044/0045). Register claims the Source + ownership; SyncLoop is the Observe loop.
		cctx, cancel := context.WithCancel(ctx)
		e.cancel = cancel
		go homegate.Supervise(cctx, r.homeDeps, c.Source.Name, host.Register, func(sctx context.Context) error {
			return host.SyncLoop(sctx, interval)
		})
		r.log.Info("connector registry: connector enabled", "name", c.Name, "class", c.Class,
			"source", c.Source.Name, "interval", interval, "actions", e.actionNames)
	} else {
		r.log.Info("connector registry: connector enabled", "name", c.Name, "class", c.Class, "actions", e.actionNames)
	}
	r.connEntries[c.Name] = e
	r.setStatus(key, true, "")
}

func (r *Registry) disableConnectorLocked(ctx context.Context, name string, e *entry) {
	if e.cancel != nil {
		e.cancel() // stop the supervise/SyncLoop goroutine (syncer class only)
	}
	// The dispatch entries go with it. A name left registered against a torn-down host is worse
	// than an absent one: it dispatches to a closed connection instead of failing to resolve.
	for _, an := range e.actionNames {
		r.plugins.DeregisterAction(an)
	}
	// Deregister releases the Source claim — meaningless for an action-class Connector, which
	// never claimed one, and harmless (the host has no registration to release).
	if err := e.host.Deregister(ctx); err != nil {
		r.log.Warn("connectorregistry: connector deregister", "name", name, "err", err)
	}
	if e.conn != nil {
		e.conn.Close()
	}
	delete(r.connEntries, name)
	r.clearStatus("connector/" + name)
	r.log.Info("connector registry: connector disabled", "name", name)
}

// ── capability dependency resolution (ADR-0104) ──────────────────────────────

// providerIndex counts, per capability class, how many providers fall in one bucket.
type providerIndex map[string]int

// resolution is the replica-consistent view a consumer resolves against. verified counts the
// providers a leader-only verification pass CONFIRMED back each capability (ADR-0104 D1) — the
// ONLY ones that satisfy a requirement. unverified counts declared-but-unconfirmed providers,
// used solely to enrich the pending reason (§1.8: "declared but rejected" vs "none declared"),
// never to satisfy.
//
// Built from the store (declared providers + the verification projection), so it is identical on
// every replica — the Actuator loop runs everywhere, the Connector loop leader-only, and only a
// store read gives the same view (the D3 routing hazard). It is health-independent: a provider
// counts on its last-CONFIRMED verdict, never on whether this pass could dial it (a dial-driven
// count would collapse ≥2 → 1 on a transient blip — a §2.4 precedence-by-liveness).
type resolution struct {
	verified   providerIndex
	unverified providerIndex
}

// buildProviderIndex splits every declared provider's governed `provides` into verified vs
// unverified by the store's verification projection (ADR-0104 D1). A phantom/unconfirmed provider
// lands in unverified and contributes 0 to satisfaction — fail closed.
func (r *Registry) buildProviderIndex(ctx context.Context) (resolution, error) {
	verifs, err := r.store.ListProviderVerifications(ctx)
	if err != nil {
		return resolution{}, err
	}
	verified := make(map[string]bool, len(verifs))
	for _, v := range verifs {
		if v.Verified {
			verified[v.Kind+"/"+v.Name] = true
		}
	}
	res := resolution{verified: providerIndex{}, unverified: providerIndex{}}
	tally := func(kind, name string, provides []string) {
		bucket := res.unverified
		if verified[kind+"/"+name] {
			bucket = res.verified
		}
		for _, tok := range provides {
			bucket[tok]++
		}
	}
	conns, err := r.store.ListConnectors(ctx)
	if err != nil {
		return resolution{}, err
	}
	for _, c := range conns {
		if len(c.Provides) > 0 {
			tally("connector", c.Name, c.Provides)
		}
	}
	acts, err := r.store.ListActuators(ctx)
	if err != nil {
		return resolution{}, err
	}
	for _, a := range acts {
		if len(a.Provides) > 0 {
			tally("actuator", a.Name, a.Provides)
		}
	}
	return res, nil
}

// ── provider verification (leader-only; ADR-0104 D1) ─────────────────────────

// classifyRequires resolves a declaration's required capabilities against the resolution (ADR-0104
// D3). Fails CLOSED + OBSERVABLE (§1.8), never a silent tiebreak (§2.4): 0 verified with a
// declared-but-rejected provider → points at the provider (descent, §1.8); 0 verified and none
// declared → unmet; exactly 1 → bound; ≥2 → ambiguous (pending until an estate binding, follow-up).
func classifyRequires(requires []string, res resolution) (ok bool, reason string) {
	for _, tok := range requires {
		switch n := res.verified[tok]; {
		case n == 0 && res.unverified[tok] > 0:
			return false, fmt.Sprintf("no verified provider for '%s': %d declared but failed/pending verification — inspect provider status (§1.8)", tok, res.unverified[tok])
		case n == 0:
			return false, "unmet dependency: no provider for '" + tok + "'"
		case n >= 2:
			return false, fmt.Sprintf("ambiguous: %d providers for '%s'; add an estate binding (ADR-0104 follow-up)", n, tok)
		}
	}
	return true, ""
}

// ResolveCapabilityAction returns the resolve-Action name of the single VERIFIED provider of a
// capability class (ADR-0105) — the mapping the orchestration invokes at dispatch.
//
// The name is READ from the provider's advertisement (ActionDecl.implements, ADR-0140 D1), never
// derived. It used to be `<pluginIdentity>/<class>-resolve`, string-concatenated here and then
// required to exist: core dictating names inside a namespace it does not own, so a provider whose
// Action was called anything else could not serve the class no matter what its Manifest said. The
// convention is DELETED, not kept as a fallback — "use the advertisement, else the old name" is a
// silent precedence rule (§2.4) that would keep unmigrated providers invisibly working until the
// day one of them did not.
//
// Fails CLOSED, with the three failures kept distinct because their fixes are (§1.8, ADR-0140 D5):
// no verified provider → look at the provider; ≥2 with no binding → look at the estate; verified
// but advertising no implementation → look at the plugin.
func (r *Registry) ResolveCapabilityAction(ctx context.Context, capClass string) (string, error) {
	verifs, err := r.store.ListProviderVerifications(ctx)
	if err != nil {
		return "", err
	}
	// The advertised implementations of each VERIFIED provider, keyed like the verification rows.
	implements := make(map[string]map[string]string, len(verifs))
	for _, v := range verifs {
		if v.Verified {
			implements[v.Kind+"/"+v.Name] = v.Implements
		}
	}
	type provider struct {
		name        string // the declaration name — what an estate binding would name
		actionNames []string
		implements  map[string]string
	}
	var providers []provider
	conns, err := r.store.ListConnectors(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range conns {
		if impl, ok := implements["connector/"+c.Name]; ok && contains(c.Provides, capClass) {
			providers = append(providers, provider{c.Name, c.ActionNames, impl})
		}
	}
	acts, err := r.store.ListActuators(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range acts {
		if impl, ok := implements["actuator/"+a.Name]; ok && contains(a.Provides, capClass) {
			providers = append(providers, provider{a.Name, a.ActionNames, impl})
		}
	}
	switch len(providers) {
	case 0:
		return "", fmt.Errorf("no verified provider for capability %q", capClass)
	case 1:
		p := providers[0]
		action := p.implements[capClass]
		if action == "" {
			// The third failure (ADR-0140 D5). Today this is the shape a provider is in when it
			// fulfils the class some other way — through a per-kind Workflow map, which is not
			// invocable as an Action — or when it has not advertised `implements` at all. Both
			// are "ask the plugin", and both used to surface as a name that did not match, which
			// pointed at the wrong thing entirely.
			return "", fmt.Errorf("capability %q provider %q is verified but its Manifest advertises no Action "+
				"implementing the class (ActionDecl.implements) — the operator grants the class; the plugin "+
				"declares which of its Actions IS the class (ADR-0140 D1)", capClass, p.name)
		}
		if !contains(p.actionNames, action) {
			// Advertised but not admitted: the plugin says this Action implements the class, and
			// the estate did not register it as dispatchable, so there is no dispatch-table entry
			// to route to. A grant gap, not a plugin gap — say so.
			return "", fmt.Errorf("capability %q provider %q advertises %q as its implementation, but that Action "+
				"is not in the declaration's actionNames, so it is not dispatchable — add it (§1.5: the Manifest "+
				"advertises, the grant is truth)", capClass, p.name, action)
		}
		return action, nil
	default:
		return "", fmt.Errorf("ambiguous: %d verified providers for capability %q; add an estate binding (ADR-0105 D5)", len(providers), capClass)
	}
}

// ResolveCapabilityActuator returns the ACTUATOR name of the single VERIFIED provider of a
// capability class (ADR-0140 D4) — the Actuator-shaped sibling of ResolveCapabilityAction, used by
// a Trigger or Baseline that names a class instead of an Actuator.
//
// No advertisement is read here, and that asymmetry is the design rather than an omission. An
// Action-shaped class needs `implements` because the plugin owns the Action's NAME inside its own
// namespace; an Actuator-shaped class does not, because the provider DECLARATION IS the Actuator —
// its name is the operator's, granted in CaC, and core is not deriving anything. That is also why a
// dial-less provider can serve this form (ADR-0138 D5) while it cannot serve the Action-shaped one.
//
// ACTUATORS ONLY. A Connector may advertise a class, but this form resolves to something
// DISPATCHABLE and a Connector is not — so it must not be a candidate here. The estate loader
// refuses the same shape at declaration; this is the runtime half.
//
// Fails CLOSED: 0 verified providers → error; ≥2 → ambiguous, never a silent tiebreak (§2.4).
func (r *Registry) ResolveCapabilityActuator(ctx context.Context, capClass string) (string, error) {
	verifs, err := r.store.ListProviderVerifications(ctx)
	if err != nil {
		return "", err
	}
	verified := make(map[string]bool, len(verifs))
	for _, v := range verifs {
		if v.Verified {
			verified[v.Kind+"/"+v.Name] = true
		}
	}
	acts, err := r.store.ListActuators(ctx)
	if err != nil {
		return "", err
	}
	var names []string
	for _, a := range acts {
		if verified["actuator/"+a.Name] && contains(a.Provides, capClass) {
			names = append(names, a.Name)
		}
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("no verified Actuator provides capability %q", capClass)
	case 1:
		return names[0], nil
	default:
		sort.Strings(names)
		return "", fmt.Errorf("ambiguous: %d verified Actuators provide capability %q (%v); add an estate binding "+
			"(ADR-0105 D5)", len(names), capClass, names)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ── provider verification (leader-only; ADR-0104 D1) ─────────────────────────

// verifyResult is a provider's manifest-verification outcome. The distinction between a STRUCTURAL
// phantom and a TRANSIENT unreachable is load-bearing (§2.4/D3, guardian Finding 1): a structural
// mismatch may zero a provider's verdict, but a transient dial/fetch failure must NEVER drop an
// already-confirmed provider from the count — else a blip in the leader's pass silently collapses
// ≥2 → 1 and auto-binds a consumer (precedence-by-liveness). Health is never a binding input.
type verifyResult int

const (
	provVerified     verifyResult = iota // manifest fetched; advertises every declared capability
	provAttested                         // dial-less; corroborated by DECLARED mechanisms (ADR-0138 D5)
	provPhantom                          // STRUCTURAL: manifest fetched; a declared capability absent
	provUnverifiable                     // STABLE: dial-less AND no declared mechanism — an unbacked claim
	provUnreachable                      // TRANSIENT: dial/fetch failed — must not drop a prior verdict
)

// Verification BASIS — how a verdict was reached, recorded so §1.8 can distinguish a provider whose
// running binary was asked from one whose declaration was read. They are not equally strong and the
// surface must not pretend otherwise.
const (
	basisManifest    = "manifest"    // the plugin's own runtime advertisement was fetched and checked
	basisDeclaration = "declaration" // dial-less: no Manifest exists to fetch (ADR-0138 D5)
)

// RunProviderVerification is the leader-only loop verifying every declared provider's Manifest
// against its declared `provides` and persisting the outcome. Appended to the leader-gated
// controllers (the sole writer of graph.capability_provider).
func (r *Registry) RunProviderVerification(ctx context.Context) {
	for {
		r.ReconcileProviderVerification(ctx)
		select {
		case <-time.After(r.interval):
		case <-ctx.Done():
			return
		}
	}
}

// ReconcileProviderVerification checks each declared provider's advertised Manifest capabilities
// against its declared `provides`. A STRUCTURAL mismatch → verified=false (phantom, §1.5), does not
// count. A TRANSIENT dial/fetch failure → the last-CONFIRMED verdict is PRESERVED (never dropped by
// a blip — §2.4/D3/Finding-1); a never-verified provider stays fail-closed but is labeled pending,
// not phantom. Rows for undeclared providers are pruned.
//
// NOTE (contract shape, ADR-0104 D1): this checks capability-TOKEN membership only. Capability
// classes today are sovereign-port verb shapes with NO per-class JSON-Schema contract and NO
// enforced protocol-version gate in core, so there is no capability contract hash to verify here
// yet — enforcing capability verb-shape compatibility (a per-class hash and/or a blocking
// min/max_protocol check) is booked hardening, not performed in this pass. No estate declares
// `provides` in such a class today.
func (r *Registry) ReconcileProviderVerification(ctx context.Context) {
	conns, err := r.store.ListConnectors(ctx)
	if err != nil {
		r.log.Warn("connectorregistry: verify: list connectors", "err", err)
		return
	}
	acts, err := r.store.ListActuators(ctx)
	if err != nil {
		r.log.Warn("connectorregistry: verify: list actuators", "err", err)
		return
	}

	type prov struct {
		kind, name, addr string
		provides         []string
		// mechanisms counts the concrete things the declaration says it can DO — the per-kind
		// Workflow maps and its dispatchable Actions. It is the corroboration a dial-less
		// provider is verified against (ADR-0138 D5); ignored entirely when a Manifest can be
		// fetched, because then the plugin speaks for itself.
		mechanisms int
	}
	var provs []prov
	for _, c := range conns {
		if len(c.Provides) > 0 {
			provs = append(provs, prov{"connector", c.Name, c.Address, c.Provides,
				len(c.Provisions) + len(c.Remediates) + len(c.Decommissions) + len(c.ActionNames)})
		}
	}
	for _, a := range acts {
		if len(a.Provides) > 0 {
			provs = append(provs, prov{"actuator", a.Name, a.Address, a.Provides,
				len(a.Provisions) + len(a.Remediates) + len(a.Decommissions) + len(a.ActionNames)})
		}
	}

	// Existing rows: used both to prune undeclared providers and to preserve a prior verdict
	// across a transient unreachable (Finding 1).
	existing := map[string]bool{}
	declared := make(map[string]bool, len(provs))
	for _, p := range provs {
		declared[p.kind+"/"+p.name] = true
	}
	if rows, err := r.store.ListProviderVerifications(ctx); err == nil {
		for _, v := range rows {
			key := v.Kind + "/" + v.Name
			if !declared[key] {
				if derr := r.store.DeleteProviderVerification(ctx, v.Kind, v.Name); derr != nil {
					r.log.Warn("connectorregistry: verify: prune", "provider", key, "err", derr)
				}
				continue
			}
			existing[key] = true
		}
	}

	for _, p := range provs {
		key := p.kind + "/" + p.name
		res, reason, impl := r.verifyProvider(ctx, p.addr, p.provides, p.mechanisms)
		switch res {
		case provVerified:
			r.recordVerification(ctx, p.kind, p.name, true, "", impl, basisManifest)
			r.log.Info("connector registry: provider verified", "provider", key, "provides", p.provides, "implements", impl)
		case provAttested:
			// Counts, on a weaker and RECORDED basis (ADR-0138 D5). No implements map: a
			// dial-less provider advertises no Manifest, so it has no Action-shaped
			// implementation to record and an Action-shaped class fails closed at resolve.
			r.recordVerification(ctx, p.kind, p.name, true, "", nil, basisDeclaration)
			r.log.Info("connector registry: provider attested from its declaration (dial-less)",
				"provider", key, "provides", p.provides, "mechanisms", p.mechanisms)
		case provPhantom, provUnverifiable:
			r.recordVerification(ctx, p.kind, p.name, false, reason, nil, "")
			r.log.Warn("connector registry: provider REJECTED (not counted)", "provider", key, "reason", reason)
		case provUnreachable:
			if existing[key] {
				// Preserve the last-CONFIRMED verdict — a transient blip must not drop an
				// established provider from the count (health-independence, §2.4/D3/Finding-1).
				// The advertised implementations are preserved with it, and for the same reason:
				// re-resolving a capability must not depend on whether this pass could dial.
				r.log.Warn("connector registry: provider verification unreachable; preserving last-known verdict", "provider", key, "reason", reason)
			} else {
				// Never verified → fail closed, labeled pending (NOT a structural phantom).
				r.recordVerification(ctx, p.kind, p.name, false, "verification pending (unreachable): "+reason, nil, "")
			}
		}
	}
}

func (r *Registry) recordVerification(ctx context.Context, kind, name string, verified bool, reason string, implements map[string]string, basis string) {
	if err := r.store.UpsertProviderVerification(ctx, kind, name, verified, reason, implements, basis); err != nil {
		r.log.Warn("connectorregistry: verify: upsert", "provider", kind+"/"+name, "err", err)
	}
}

// verifyProvider fetches the plugin's Manifest and classifies the outcome (see verifyResult).
// On success it also returns the class→Action implementations the Manifest advertised, RESTRICTED
// to the classes the operator granted (ADR-0140 D1).
func (r *Registry) verifyProvider(ctx context.Context, addr string, provides []string, mechanisms int) (verifyResult, string, map[string]string) {
	if addr == "" {
		// ── The dial-less form (ADR-0138 D5) ─────────────────────────────────────────────
		// An EE-Job Actuator has no dial address BY CONSTRUCTION — ansible is subprocess-only
		// because of the GPLv3 boundary (§3) — so there is no Manifest to fetch, and treating
		// that as a failed verification made capability routing structurally unavailable to
		// exactly the tools `configmgmt` exists for. ADR-0135 D3 shipped a route its own
		// flagship provider could never satisfy; every unit test passed because they resolve
		// through a fake.
		//
		// What CAN be corroborated is whether the claim is backed by anything. A declaration
		// that advertises a class and declares no mechanism at all is a bare assertion, and it
		// stays refused. One that names a per-kind Workflow or a dispatchable Action has
		// committed to something the estate loader already validated — a `provisions`/
		// `decommissions` entry must name a Workflow that EXISTS, and `remediates` must sit on
		// a provider that advertises a class. So the attestation is not self-certifying: it is
		// checked against a different part of the tree than the one making the claim.
		//
		// It is deliberately WEAKER than a Manifest check and is recorded as such (basis), so
		// the surface never implies a running binary was asked when it was not (§1.8). It is
		// also weaker in a second, load-bearing way: an ACTION-shaped class needs a
		// Manifest-advertised `implements` (ADR-0140 D1), which a dial-less provider cannot
		// supply. Such a provider therefore attests fine and then fails CLOSED at
		// ResolveCapabilityAction with "advertises no Action implementing the class" — the
		// honest outcome, since a subprocess genuinely cannot serve an Action-shaped class.
		// In practice attestation admits the Workflow-shaped classes, which is the point.
		if mechanisms == 0 {
			return provUnverifiable, "provider has no dial address and declares no mechanism — a capability claim " +
				"backed by no provisions/remediates/decommissions map and no actionNames is an assertion with " +
				"nothing behind it (ADR-0138 D5)", nil
		}
		return provAttested, "", nil
	}
	m, err := r.manifest(ctx, addr)
	if err != nil {
		return provUnreachable, "manifest fetch failed: " + err.Error(), nil
	}
	advertised := make(map[string]bool, len(m.Capabilities))
	for _, c := range m.Capabilities {
		advertised[c] = true
	}
	granted := make(map[string]bool, len(provides))
	for _, tok := range provides {
		if !advertised[tok] {
			return provPhantom, fmt.Sprintf("phantom provider: declares provides %q but its Manifest does not advertise it (§1.5)", tok), nil
		}
		granted[tok] = true
	}

	// The class→Action implementations, filtered by the grant. An advertisement for a class the
	// operator did NOT grant is dropped, not honored: the Manifest advertises, the grant is truth
	// (§1.5) — a plugin cannot admit itself to a class by claiming an implementation for it.
	//
	// Deliberately NOT a phantom check. ADR-0140 D1 proposed treating a provider that advertises
	// no implementation for a class it claims as a phantom, but that was written before its own D3
	// separated the three Step shapes: `provisioning` is fulfilled through a per-kind `provisions:`
	// Workflow map and has no resolve Action at all, so the rule as drafted would phantom every
	// provisioning provider in the estate (awsec2, crossplane, opentofu, vcenter) and take the
	// whole build path down. A missing implementation is refused where it is USED —
	// ResolveCapabilityAction's third failure — not where it is merely absent.
	var impl map[string]string
	for _, a := range m.Actions {
		class := a.Implements
		if class == "" {
			continue
		}
		if !granted[class] {
			r.log.Info("connector registry: ignoring advertised implementation for an ungranted capability class",
				"action", a.Name, "class", class, "granted", provides)
			continue
		}
		if impl == nil {
			impl = map[string]string{}
		}
		if prior, dup := impl[class]; dup {
			// Two Actions claiming to BE the same class is unresolvable without a tiebreak, and
			// a tiebreak is what §2.4 forbids. Fail the whole verification rather than pick.
			return provPhantom, fmt.Sprintf("provider advertises two implementations of capability %q (%q and %q); "+
				"a class has one implementation per provider and core must not choose (§2.4)", class, prior, a.Name), nil
		}
		impl[class] = a.Name
	}
	return provVerified, "", impl
}

// ── grant construction ──────────────────────────────────────────────────────

func connectorGrant(c types.Connector) pluginhost.Grant {
	return pluginhost.Grant{
		PluginIdentity:               c.PluginIdentity,
		Tier:                         pluginhost.Tier(c.Tier),
		Source:                       c.Source, // desired half only (ValidateConnector rejected homing)
		FacetNamespaces:              c.FacetNamespaces,
		AuthoritativeFacetNamespaces: c.AuthoritativeFacetNamespaces,
		LabelKeys:                    c.LabelKeys,
		IdentitySchemes:              c.IdentitySchemes,
		TombstoneSchemes:             c.TombstoneSchemes,
		EmitterName:                  c.EmitterName,
	}
}

// actuatorGrant builds the govern grant for a plugin Actuator. An Actuator binds no SoR
// Source and owns nothing; the nominal Source (name = actuator name) only names the govern
// identity for a Site-relayed host (ADR-0049), never a RegisterSource.
func actuatorGrant(a types.Actuator) pluginhost.Grant {
	return pluginhost.Grant{
		PluginIdentity: a.PluginIdentity,
		Tier:           pluginhost.Tier(a.Tier),
		Source:         types.Source{Kind: a.Name, Name: a.Name},
		// The declared MF3 bounds (ADR-0103 CaC grant, ADR-0117 D3a). Empty stays empty —
		// a pure executor writes no facet — but a declared ansible-class Actuator can now
		// carry the same bounded grant the boot registration inlines, instead of silently
		// being a weaker Actuator whose fact write-back is refused.
		FacetNamespaces: a.FacetNamespaces,
		IdentitySchemes: a.IdentitySchemes,
		// The label write-back grant (ADR-0047 §4). Without it a declared Actuator is strictly
		// weaker than the boot block it replaces, and the narrowing is INVISIBLE — an ungranted
		// label key is dropped at the governor, never refused.
		LabelKeys: a.LabelKeys,
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
