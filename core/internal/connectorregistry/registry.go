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
	return &Registry{
		store: store, plugins: plugins, homeDeps: homeDeps, plans: plans, dial: dial, interval: interval, log: log,
		actEntries: map[string]*entry{}, connEntries: map[string]*entry{}, status: map[string]Status{},
	}
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
		r.enableActuatorLocked(a, spec)
	}
}

func (r *Registry) enableActuatorLocked(a types.Actuator, spec string) {
	key := "actuator/" + a.Name
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
		JobCommand: a.JobCommand, Image: a.Image, MCP: a.MCP,
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
		r.enableConnectorLocked(ctx, c, spec)
	}
}

func (r *Registry) enableConnectorLocked(ctx context.Context, c types.Connector, spec string) {
	key := "connector/" + c.Name
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
