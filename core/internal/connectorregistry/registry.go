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

// ManifestFetcher returns the capability tokens a plugin at addr advertises in its Manifest
// — the §1.5 VERIFICATION input for provider verification (ADR-0104 D1), distinct from the
// operator's declared `provides`. Injectable so tests need no live plugin.
type ManifestFetcher func(ctx context.Context, addr string) ([]string, error)

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
func (r *Registry) dialManifest(ctx context.Context, addr string) ([]string, error) {
	conn, err := r.dial(addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := pluginv1.NewPluginServiceClient(conn).GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetManifest().GetCapabilities(), nil
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
		r.enableActuatorLocked(a, spec, res)
	}
}

func (r *Registry) enableActuatorLocked(a types.Actuator, spec string, res resolution) {
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
	}
	host := pluginhost.New(r.store, client, grant, r.log)
	if r.plans != nil {
		host = host.UsePlanStore(r.plans)
	}
	pa := orchestrate.PluginActuator{
		Host: host, DryRunnable: a.DryRunnable, Grant: grant, PlanStore: r.plans,
		JobCommand: a.JobCommand, Image: a.Image, MCP: a.MCP, Requires: a.Requires,
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
		if err := r.plugins.RegisterAction(an, orchestrate.PluginAction{Host: host, DryRunnable: a.DryRunnable}); err != nil {
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
		if c.Class != types.ConnectorSyncer {
			r.setStatus("connector/"+c.Name, false, "class "+c.Class+" not yet wired (ADR-0103 slice-1 is syncer-only)")
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
	host := pluginhost.New(r.store, pluginv1.NewPluginServiceClient(conn), connectorGrant(c), r.log)
	interval := time.Duration(c.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// Home-gated supervised Syncer under a per-connector child context (single-writer,
	// ADR-0044/0045). Register claims the Source + ownership; SyncLoop is the Observe loop.
	cctx, cancel := context.WithCancel(ctx)
	go homegate.Supervise(cctx, r.homeDeps, c.Source.Name, host.Register, func(sctx context.Context) error {
		return host.SyncLoop(sctx, interval)
	})
	r.connEntries[c.Name] = &entry{conn: conn, cancel: cancel, host: host, specJSON: spec}
	r.setStatus(key, true, "")
	r.log.Info("connector registry: connector enabled", "name", c.Name, "source", c.Source.Name, "interval", interval)
}

func (r *Registry) disableConnectorLocked(ctx context.Context, name string, e *entry) {
	if e.cancel != nil {
		e.cancel() // stop the supervise/SyncLoop goroutine
	}
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
// capability class (ADR-0105) — the mapping the orchestration invokes at dispatch. Fails CLOSED:
// 0 verified providers → error; ≥2 → ambiguous (an estate binding must name one, ADR-0105 D5), never
// a silent tiebreak (§2.4). The resolve Action is the provider's `<pluginIdentity>/<class>-resolve`
// (the frozen <plugin>/<op> convention), and it must be one of the provider's declared ActionNames.
func (r *Registry) ResolveCapabilityAction(ctx context.Context, capClass string) (string, error) {
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
	type provider struct {
		pluginIdentity string
		actionNames    []string
	}
	var providers []provider
	conns, err := r.store.ListConnectors(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range conns {
		if verified["connector/"+c.Name] && contains(c.Provides, capClass) {
			providers = append(providers, provider{c.PluginIdentity, c.ActionNames})
		}
	}
	acts, err := r.store.ListActuators(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range acts {
		if verified["actuator/"+a.Name] && contains(a.Provides, capClass) {
			providers = append(providers, provider{a.PluginIdentity, a.ActionNames})
		}
	}
	switch len(providers) {
	case 0:
		return "", fmt.Errorf("no verified provider for capability %q", capClass)
	case 1:
		want := providers[0].pluginIdentity + "/" + capClass + "-resolve"
		if !contains(providers[0].actionNames, want) {
			return "", fmt.Errorf("capability %q provider %q does not declare its resolve Action %q", capClass, providers[0].pluginIdentity, want)
		}
		return want, nil
	default:
		return "", fmt.Errorf("ambiguous: %d verified providers for capability %q; add an estate binding (ADR-0105 D5)", len(providers), capClass)
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
	provPhantom                          // STRUCTURAL: manifest fetched; a declared capability absent
	provUnverifiable                     // STABLE: no dial address (EE-Job) — not manifest-verifiable
	provUnreachable                      // TRANSIENT: dial/fetch failed — must not drop a prior verdict
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
	}
	var provs []prov
	for _, c := range conns {
		if len(c.Provides) > 0 {
			provs = append(provs, prov{"connector", c.Name, c.Address, c.Provides})
		}
	}
	for _, a := range acts {
		if len(a.Provides) > 0 {
			provs = append(provs, prov{"actuator", a.Name, a.Address, a.Provides})
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
		res, reason := r.verifyProvider(ctx, p.addr, p.provides)
		switch res {
		case provVerified:
			r.recordVerification(ctx, p.kind, p.name, true, "")
			r.log.Info("connector registry: provider verified", "provider", key, "provides", p.provides)
		case provPhantom, provUnverifiable:
			r.recordVerification(ctx, p.kind, p.name, false, reason)
			r.log.Warn("connector registry: provider REJECTED (not counted)", "provider", key, "reason", reason)
		case provUnreachable:
			if existing[key] {
				// Preserve the last-CONFIRMED verdict — a transient blip must not drop an
				// established provider from the count (health-independence, §2.4/D3/Finding-1).
				r.log.Warn("connector registry: provider verification unreachable; preserving last-known verdict", "provider", key, "reason", reason)
			} else {
				// Never verified → fail closed, labeled pending (NOT a structural phantom).
				r.recordVerification(ctx, p.kind, p.name, false, "verification pending (unreachable): "+reason)
			}
		}
	}
}

func (r *Registry) recordVerification(ctx context.Context, kind, name string, verified bool, reason string) {
	if err := r.store.UpsertProviderVerification(ctx, kind, name, verified, reason); err != nil {
		r.log.Warn("connectorregistry: verify: upsert", "provider", kind+"/"+name, "err", err)
	}
}

// verifyProvider fetches the plugin's Manifest and classifies the outcome (see verifyResult).
func (r *Registry) verifyProvider(ctx context.Context, addr string, provides []string) (verifyResult, string) {
	if addr == "" {
		return provUnverifiable, "provider has no dial address (EE-Job providers are not yet manifest-verifiable, ADR-0104 D1)"
	}
	caps, err := r.manifest(ctx, addr)
	if err != nil {
		return provUnreachable, "manifest fetch failed: " + err.Error()
	}
	advertised := make(map[string]bool, len(caps))
	for _, c := range caps {
		advertised[c] = true
	}
	for _, tok := range provides {
		if !advertised[tok] {
			return provPhantom, fmt.Sprintf("phantom provider: declares provides %q but its Manifest does not advertise it (§1.5)", tok)
		}
	}
	return provVerified, ""
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
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
