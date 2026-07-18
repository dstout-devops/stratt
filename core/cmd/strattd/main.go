// Command strattd is the Stratt control-plane server (charter §3): the
// graph-store frontend, the OpenAPI-first API, the Temporal worker for Run
// Workflows, the K8s Job dispatcher, and the Phase-0 vCenter Syncer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dstout-devops/stratt/core/internal/actions"
	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/api"
	"github.com/dstout-devops/stratt/core/internal/audit"
	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/baselines"
	"github.com/dstout-devops/stratt/core/internal/cellrouter"
	"github.com/dstout-devops/stratt/core/internal/compiler"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/emitters"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/evidencestore"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/homegate"
	"github.com/dstout-devops/stratt/core/internal/leader"
	"github.com/dstout-devops/stratt/core/internal/notify"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/planstore"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/scim"
	"github.com/dstout-devops/stratt/core/internal/sitegw"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
	"github.com/dstout-devops/stratt/core/internal/siterelay"
	"github.com/dstout-devops/stratt/core/internal/statebackend"
	"github.com/dstout-devops/stratt/core/internal/triggerengine"
	"github.com/dstout-devops/stratt/core/internal/triggers"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(ctx, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("strattd exiting", "error", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// leaderLeaseName returns the Cell-scoped leader lease name (ADR-0044): the
// legacy "strattd-leader" for the built-in local Cell, "strattd-leader-<cell>"
// for a named Cell so peer Cells sharing a namespace never contend one lease.
func leaderLeaseName(cell string) string {
	if cell == "" || cell == types.LocalCell {
		return "strattd-leader"
	}
	return "strattd-leader-" + cell
}

// isAuthzHome decides whether THIS daemon's Cell is the authz home — the sole
// writer of the shared OpenFGA tuple store (ADR-0044 slice 4). Derived from the
// in-memory CaC Cell set (not a DB read, which would race the reconcile). A pure
// single-Cell estate (no declared Cells) makes the built-in 'local' Cell the
// trivial authz writer; a named fleet must not run a 'local' daemon (it would be
// a second writer) — loud-fail. Changing the designation requires a restart.
func isAuthzHome(cellID string, cells []types.Cell) (bool, error) {
	if len(cells) == 0 {
		return cellID == types.LocalCell, nil
	}
	if cellID == types.LocalCell {
		return false, fmt.Errorf("STRATT_CELL_ID is 'local' but %d named Cells are declared; set STRATT_CELL_ID to this Cell's name", len(cells))
	}
	for _, c := range cells {
		if c.Name == cellID {
			return c.AuthzHome, nil
		}
	}
	return false, nil // this Cell isn't in the declared fleet → never authz-home
}

// reconcileDispatchScope loud-fails when this daemon's effective NATS scope
// token (env-derived — the ONLY source the DB-less Site agents can share) does
// not match its Cell's CaC-declared DispatchPrefix (ADR-0044 slice 6). The
// declared prefix is desired state; the deployed env is the runtime input; a
// divergence is neither silently resolved by precedence nor tolerated (§2.4
// exactly-one-answer) — it means the hub and its agents would scope differently,
// so the daemon refuses to boot rather than serve on subjects the agents can't
// find. A Cell absent from the declared fleet has no DispatchPrefix to reconcile
// (the env token stands alone); 'local' in a named fleet already loud-fails in
// isAuthzHome.
func reconcileDispatchScope(cellID, effective string, cells []types.Cell) error {
	for _, c := range cells {
		if c.Name != cellID {
			continue
		}
		declared := types.CellScopeToken(cellID, c.DispatchPrefix)
		if declared != effective {
			return fmt.Errorf(
				"NATS dispatch scope mismatch for Cell %q: effective %q (from STRATT_CELL_ID / STRATT_CELL_DISPATCH_PREFIX) != CaC-declared %q (graph.cell.dispatch_prefix); the hub and its Site agents must scope identically — align the env with the declaration",
				cellID, effective, declared)
		}
		return nil
	}
	return nil
}

// splitNonEmpty splits a comma-separated env value into trimmed, non-empty
// entries (e.g. STRATT_SALT_EVENT_TAGS="salt/minion/,salt/job/").
func splitNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func run(ctx context.Context, log *slog.Logger) error {
	// ── graph plane ──────────────────────────────────────────────────────
	store, err := graph.Connect(ctx, env("STRATT_DATABASE_URL", "postgres://stratt:stratt-dev@localhost:5432/stratt"))
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info("graph store ready (migrations applied)")

	// ── control-plane Cell identity (ADR-0044) ───────────────────────────
	// A Cell is a region-local single-writer control-plane shard. STRATT_CELL_ID
	// stamps this daemon's Cell into write provenance (prov_cell) and, for a
	// named Cell, into the collision-prone shared-name control resources (leader
	// lease, Temporal namespace/queue) so a peer Cell sharing substrate cannot
	// collide. The default "local" Cell keeps every name byte-identical to the
	// pre-Cells control plane. (Cross-Cell federation, homing semantics, and
	// NATS-subject scoping are later ADR-0044 slices.)
	cellID := env("STRATT_CELL_ID", types.LocalCell)
	store.SetCell(cellID)
	// Active environment (ADR-0057): a logical dev/staging/prod slice WITHIN this
	// Cell. Empty = unscoped (reconciles every declaration, byte-identical to
	// pre-ADR-0057). When set, the reconcile applies + prunes only its slice.
	store.SetEnvironment(env("STRATT_ENVIRONMENT", ""))
	// scopeTok is the Cell's NATS subject/stream scope token — the ONE string
	// the hub and every Site agent derive identically from shared env so the two
	// ends exchange on the same subjects (ADR-0044 slice 6). "" for LocalCell
	// keeps every NATS name byte-identical; a named Cell scopes the run-event,
	// emitter, notice, dispatch, and result planes so peers sharing a NATS
	// cluster never cross-wire. STRATT_CELL_DISPATCH_PREFIX overrides the default
	// (the Cell name); reconciled against the CaC-declared DispatchPrefix below.
	scopeTok := types.CellScopeToken(cellID, os.Getenv("STRATT_CELL_DISPATCH_PREFIX"))
	if !types.ValidCellScopeToken(scopeTok) {
		return fmt.Errorf("NATS scope token %q (from STRATT_CELL_ID=%q / STRATT_CELL_DISPATCH_PREFIX) is not NATS-safe: use lower-case alphanumeric + '-', no '.'/'*'/'>'", scopeTok, cellID)
	}
	siteproto.SetScope(scopeTok)
	temporalNamespaceDefault := "default"
	if cellID != types.LocalCell {
		orchestrate.TaskQueue = orchestrate.TaskQueue + "-" + cellID
		temporalNamespaceDefault = "stratt-" + cellID
		log.Info("control-plane cell", "cell", cellID, "taskQueue", orchestrate.TaskQueue, "natsScope", scopeTok)
	}

	// Shared Intent-compile status: the desired-state controller writes each
	// pass, GET /compile serves it (§4.3 membership-delta surface, ADR-0023).
	compileStatus := &compiler.Status{}

	// Pin the shipped Contract documents (§1.5, ADR-0015). Drift between a
	// registered pin and the shipped document is blocking — the platform
	// must not boot with schemas that silently changed under their pins.
	shipped, err := contract.All()
	if err != nil {
		return err
	}
	for _, c := range shipped {
		if err := store.RegisterContract(ctx, c); err != nil {
			return err
		}
	}
	log.Info("contracts pinned", "count", len(shipped))

	// Bootstrap ownership registrations (§2.1: registration precedes writes).
	// os.kernel is written back by Runs; owned by the platform team until the
	// Blueprint compiler owns fact routing (Phase 2, charter-guardian note).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "os.kernel", OwnerKind: "team", OwnerRef: "platform",
	}); err != nil {
		return err
	}

	// ── event plane ──────────────────────────────────────────────────────
	bus, err := events.Connect(ctx, env("STRATT_NATS_URL", "nats://localhost:4222"), scopeTok)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureEmitterStream(ctx); err != nil {
		return err
	}
	if err := bus.EnsureNoticeStream(ctx); err != nil {
		return err
	}
	log.Info("event bus ready", "stream", bus.StreamName())

	// ── Site dispatch plane (§2.3, ADR-0032) ─────────────────────────────
	// The hub↔Site NATS gateway: the dispatch/result streams + liveness KV
	// remote execution loci use. Local-only Runs never touch it.
	siteGateway, err := sitegw.Connect(env("STRATT_NATS_URL", "nats://localhost:4222"), "strattd", log)
	if err != nil {
		return err
	}
	defer siteGateway.Close()
	if err := siteGateway.EnsureStreams(ctx); err != nil {
		return err
	}
	log.Info("site gateway ready", "streams", []string{siteproto.DispatchStream, siteproto.ResultStream})

	// ── orchestration plane ──────────────────────────────────────────────
	temporalClient, err := client.Dial(client.Options{
		HostPort:  env("STRATT_TEMPORAL_ADDRESS", "localhost:7233"),
		Namespace: env("STRATT_TEMPORAL_NAMESPACE", temporalNamespaceDefault),
		Logger:    tlog{log.With("component", "temporal")},
	})
	if err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	defer temporalClient.Close()

	// ── actuation plane (K8s Jobs, §3) ───────────────────────────────────
	kubeClient, err := kubeClientset()
	if err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	eeFSGroup, err := strconv.ParseInt(env("STRATT_EE_FSGROUP", "1000"), 10, 64)
	if err != nil {
		return fmt.Errorf("ee fsgroup: %w", err)
	}
	dispatcher := dispatch.New(dispatch.Config{
		Namespace: env("STRATT_K8S_NAMESPACE", "default"),
		EEImage:   env("STRATT_EE_IMAGE", "stratt-ee:dev"),
		FSGroup:   eeFSGroup,
	}, kubeClient, bus, log)

	// ── authorization seam (§2.5, ADR-0009) ─────────────────────────────
	// The CaC tuple evaluator always loads (it is the no-substrate dev path
	// and the model's semantic reference); with STRATT_OPENFGA_URL set the
	// server answers checks instead, fed the same tuples by SyncTuples —
	// two backends, one Authorizer seam, one Git source. Deny is the
	// default: with no tuples loaded, every grant-gated surface refuses.
	evaluator := &authz.TupleAuthorizer{}
	var authorizer authz.Authorizer = evaluator
	var fga *authz.OpenFGAAuthorizer
	if fgaURL := os.Getenv("STRATT_OPENFGA_URL"); fgaURL != "" {
		if fga, err = authz.NewOpenFGAAuthorizer(ctx, fgaURL); err != nil {
			return err
		}
		authorizer = fga
		log.Info("authz backend: openfga", "url", fgaURL)
	} else {
		log.Info("authz backend: in-process tuple evaluator (STRATT_OPENFGA_URL empty)")
	}

	devPrincipal := os.Getenv("STRATT_DEV_PRINCIPAL_HEADER") == "true"
	if devPrincipal {
		log.Warn("DEV PRINCIPAL MODE: X-Stratt-Principal header is trusted — dev harness only (ADR-0009)")
	}
	var oidcResolver *authz.OIDCResolver
	oidcIssuer := os.Getenv("STRATT_OIDC_ISSUER")
	oidcAudience := os.Getenv("STRATT_OIDC_AUDIENCE")
	if issuer := oidcIssuer; issuer != "" {
		// Production guard (ADR-0013, slice-5 guardian flag): an issuer
		// without an audience accepts any token the IdP ever minted for any
		// client. Skipping the audience check is a loud, explicit dev-only
		// opt-out — never a default.
		audience := oidcAudience
		if audience == "" {
			if os.Getenv("STRATT_OIDC_ALLOW_NO_AUDIENCE") != "true" {
				return fmt.Errorf("STRATT_OIDC_ISSUER is set but STRATT_OIDC_AUDIENCE is empty; set an audience or explicitly set STRATT_OIDC_ALLOW_NO_AUDIENCE=true (dev only)")
			}
			log.Warn("OIDC AUDIENCE CHECK DISABLED (STRATT_OIDC_ALLOW_NO_AUDIENCE=true) — dev harness only")
		}
		// Fail fast: a misconfigured issuer must not boot an API that 401s
		// every Bearer holder while looking healthy.
		if oidcResolver, err = authz.NewOIDCResolver(ctx, issuer, audience); err != nil {
			return err
		}
		log.Info("identity backend: oidc", "issuer", issuer, "audienceCheck", audience != "")
	} else {
		log.Info("identity backend: none (STRATT_OIDC_ISSUER empty); Bearer tokens are not accepted")
	}

	// In-tree Actuator registry (§2.3); out-of-tree Actuators arrive via the
	// plugin Contract surfaces, not this map.
	//
	// Out-of-tree Actuators arrive via the plugin Contract surfaces, not this map.
	// ansible (ADR-0051), script + webhook/notify (ADR-0046 Category A) have LEFT the
	// Apache core — ansible/script as EE-Job shims, notify/webhook as a gRPC plugin
	// Action (§1.4 — no `if <tool> {…}` in the spine). Only mcp remains in-tree
	// (registered below) pending its own extraction slice.
	registry := map[string]actuators.Actuator{}

	// In-tree Action registry (§2.2, ADR-0031): targetless typed operations shipped by
	// Connectors. cert lifecycle is the certissuer reconcile Actuator over the port
	// (ADR-0050); notify/webhook is now a gRPC plugin Action (ADR-0052); awsec2
	// create-vm is a plugin Action. No in-tree Actions remain today.
	awsPluginAddr := os.Getenv("STRATT_AWS_PLUGIN_ADDR")
	actionRegistry := actions.Registry{}
	for _, act := range []actions.Action{
		// (in-tree Actions extracted to plugins; none remain)
	} {
		actionRegistry[act.Name()] = act
	}
	log.Info("action registry ready", "actions", len(actionRegistry))

	// ── Plugin-provided Actions over the port (ADR-0047/0048 cutover) ────────
	// A plugin Action name is EXCLUSIVE with the in-tree registry and across
	// plugins (§2.4): a collision fails startup, never silently overwrites.
	pluginActions := map[string]orchestrate.PluginAction{}
	registerPluginAction := func(name string, host *pluginhost.Host, dryRunnable bool) error {
		if _, dup := pluginActions[name]; dup {
			return fmt.Errorf("plugin action %q claimed by two plugins (§2.4 exclusive)", name)
		}
		if _, inTree := actionRegistry[name]; inTree {
			return fmt.Errorf("plugin action %q collides with an in-tree Action (§2.4 exclusive)", name)
		}
		pluginActions[name] = orchestrate.PluginAction{Host: host, DryRunnable: dryRunnable}
		return nil
	}

	// A plugin Actuator name is EXCLUSIVE with the in-tree Actuator registry and
	// across plugins (§2.4): a collision fails startup, never silently overwrites.
	pluginActuators := map[string]orchestrate.PluginActuator{}
	// grant + plans travel with the actuator so Execute can build a Site-backed host
	// with identical governance (the grant never leaves the hub, ADR-0049 V1).
	registerPluginActuator := func(name string, host *pluginhost.Host, dryRunnable bool, grant pluginhost.Grant, plans *planstore.Store) error {
		if _, dup := pluginActuators[name]; dup {
			return fmt.Errorf("plugin actuator %q claimed by two plugins (§2.4 exclusive)", name)
		}
		if _, inTree := registry[name]; inTree {
			return fmt.Errorf("plugin actuator %q collides with an in-tree Actuator (§2.4 exclusive)", name)
		}
		pluginActuators[name] = orchestrate.PluginActuator{Host: host, DryRunnable: dryRunnable, Grant: grant, PlanStore: plans}
		return nil
	}

	// Ansible EE-Job (subprocess) transport (ADR-0051): the flagship Actuator over
	// the sovereign port, the SOLE ansible path (Phase 5b cutover). No gRPC dial —
	// the transport IS the K8s Job running the stratt-ansible shim. The host carries
	// only the MF3 BOUNDED grant (never a wildcard): exactly the Facet namespaces the
	// shim projects and the host.name identity scheme it correlates facts by.
	// GovernStream gates the Job's typed stdout against this grant hub-side; the gRPC
	// client is nil (govern never dials).
	{
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_ANSIBLE_PLUGIN_ID", "ansible"),
			Tier:           pluginhost.TierTrusted,
			Source:         types.Source{Kind: "ansible", Name: env("STRATT_ANSIBLE_SOURCE_NAME", "ansible")},
			FacetNamespaces: []string{
				"os.kernel",
				"os.hardening.sysctl", "os.hardening.sshd", "os.hardening.filesystem",
				"os.hardening.auditd", "os.hardening.services",
				"fileset.content", "access.grants",
			},
			IdentitySchemes: []string{"host.name"},
		}
		if _, dup := pluginActuators["ansible"]; dup {
			return fmt.Errorf("ansible EE-Job actuator collides with a registered plugin actuator (§2.4 exclusive)")
		}
		host := pluginhost.New(store, nil, grant, log) // nil client: the Job is the transport; govern uses only the grant
		pluginActuators["ansible"] = orchestrate.PluginActuator{
			Host: host, DryRunnable: true, Grant: grant,
			JobCommand: []string{env("STRATT_ANSIBLE_SHIM", "stratt-ansible")},
		}
		log.Info("ansible EE-Job actuator registered (ADR-0051 subprocess transport)", "shim", env("STRATT_ANSIBLE_SHIM", "stratt-ansible"))
	}

	// Script EE-Job (subprocess) transport (ADR-0046 Category A): the per-target
	// script-runner over the sovereign port. Effectful (no read-only capability →
	// NOT DryRunnable, so a dry-run/baseline against it is rejected at launch). Its
	// grant is EMPTY (script proposes no Facets/identity write-back — GovernStream
	// folds only the per-target ItemResults, confused-deputy gated on the resolved
	// set). No gRPC dial — the transport is the K8s Job running stratt-script.
	{
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_SCRIPT_PLUGIN_ID", "script"),
			Tier:           pluginhost.TierTrusted,
			Source:         types.Source{Kind: "script", Name: env("STRATT_SCRIPT_SOURCE_NAME", "script")},
		}
		if _, dup := pluginActuators["script"]; dup {
			return fmt.Errorf("script EE-Job actuator collides with a registered plugin actuator (§2.4 exclusive)")
		}
		host := pluginhost.New(store, nil, grant, log)
		pluginActuators["script"] = orchestrate.PluginActuator{
			Host: host, DryRunnable: false, Grant: grant,
			JobCommand: []string{env("STRATT_SCRIPT_SHIM", "stratt-script")},
		}
		log.Info("script EE-Job actuator registered (ADR-0046 subprocess transport)", "shim", env("STRATT_SCRIPT_SHIM", "stratt-script"))
	}

	// awsec2 plugin: when configured it provides BOTH the instance Syncer and the
	// create-vm Action over the port; the in-tree awsec2 is then disabled.
	var awsHost *pluginhost.Host
	if awsPluginAddr != "" {
		conn, err := grpc.NewClient(awsPluginAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("awsec2 plugin dial %s: %w", awsPluginAddr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_AWS_PLUGIN_ID", "awsec2"),
			Tier:             pluginhost.Tier(env("STRATT_AWS_TIER", "trusted")),
			Source:           types.Source{Kind: "awsec2", Name: env("STRATT_AWS_SOURCE_NAME", "awsec2"), Endpoint: os.Getenv("STRATT_AWS_ENDPOINT")},
			FacetNamespaces:  []string{"instance.compute", "instance.network", "instance.state"},
			LabelKeys:        []string{"aws.region", "aws.name"},
			IdentitySchemes:  []string{"aws.instanceId"},
			TombstoneSchemes: []string{"aws.instanceId"},
		}
		awsHost = pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		if err := registerPluginAction("awsec2/create-vm", awsHost, true); err != nil {
			return err
		}
		log.Info("awsec2 plugin actions registered", "addr", awsPluginAddr)
	}

	// notify/webhook plugin Action (ADR-0046 Category A / ADR-0052): the notification
	// delivery left the core. When configured, the stratt-notify plugin issues the POST
	// in-process, resolving the Sink's per-call url/token via the SecretBroker — the
	// core hands COORDINATES (never material) in the Envelope (§2.5). NOT DryRunnable
	// (a POST has no read-only plan). Unset ⇒ no notify/webhook Action is registered
	// and notifications fail closed (the in-tree pod Action was retired — the cutover).
	if addr := os.Getenv("STRATT_NOTIFY_PLUGIN_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("notify plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_NOTIFY_PLUGIN_ID", "notify"),
			Tier:           pluginhost.TierTrusted, // SecretBroker resolution is trusted-tier (MF-A)
			Source:         types.Source{Kind: "notify", Name: env("STRATT_NOTIFY_SOURCE_NAME", "notify")},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		if err := registerPluginAction("notify/webhook", host, false); err != nil {
			return err
		}
		log.Info("notify plugin action registered (ADR-0052 SecretBroker)", "addr", addr)
	} else {
		log.Info("no notify plugin configured (STRATT_NOTIFY_PLUGIN_ADDR empty); notifications disabled")
	}

	// mcp EE-Job transport (ADR-0053): MCP is a generic transport (charter §1.5), not
	// an in-core protocol. The stratt-mcp shim (baked into the EE-mcp image) speaks
	// JSON-RPC to the sandboxed server; the CORE keeps the seam — it resolves the
	// MCPServer declaration + rev, validates call-args against the pin, and pins each
	// rung-3 derived_contract (executeMCP). The grant Source.Name is "mcp" so a
	// derived tool schema (mcp/<server>/<tool>.input) is namespace-confined to it.
	{
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_MCP_PLUGIN_ID", "mcp"),
			Tier:           pluginhost.TierTrusted,
			Source:         types.Source{Kind: "mcp", Name: "mcp"},
		}
		if _, dup := pluginActuators["mcp"]; dup {
			return fmt.Errorf("mcp actuator collides with a registered plugin actuator (§2.4 exclusive)")
		}
		host := pluginhost.New(store, nil, grant, log)
		pluginActuators["mcp"] = orchestrate.PluginActuator{
			Host: host, DryRunnable: false, Grant: grant, MCP: true,
			JobCommand: []string{env("STRATT_MCP_SHIM", "stratt-mcp")},
			Image:      env("STRATT_EE_MCP_IMAGE", "stratt-ee-mcp:dev"),
		}
		log.Info("mcp EE-Job actuator registered (ADR-0053 generic MCP transport)", "eeImage", env("STRATT_EE_MCP_IMAGE", "stratt-ee-mcp:dev"))
	}

	// OpenTofu (ADR-0016): requires the encrypted state backend — without a
	// state key the actuator is not registered and the backend not mounted;
	// tofu Steps then fail loudly at Prepare, never plaintext local state.
	var stateHandler http.Handler
	if stateKey := os.Getenv("STRATT_STATE_KEY"); stateKey != "" {
		sb, err := statebackend.New(stateKey, store, log)
		if err != nil {
			return err
		}
		stateHandler = sb.Handler()
		if tofuPluginAddr := os.Getenv("STRATT_OPENTOFU_PLUGIN_ADDR"); tofuPluginAddr != "" {
			// Cutover (ADR-0046/0047): the opentofu Actuator runs over the sovereign
			// port — Plan/Apply/Destroy, plan-as-artifact (§8). The in-tree Actuator
			// is NOT registered (§2.4 exclusive). The plan store shares the state key
			// (the plan is content-addressed + encrypted, ADR-0047 §8); the plugin
			// derives its own TF_HTTP_PASSWORD from its own STRATT_STATE_KEY config.
			plans, err := planstore.New(stateKey, store)
			if err != nil {
				return err
			}
			conn, err := grpc.NewClient(tofuPluginAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return fmt.Errorf("opentofu plugin dial %s: %w", tofuPluginAddr, err)
			}
			defer conn.Close()
			grant := pluginhost.Grant{
				PluginIdentity: env("STRATT_OPENTOFU_PLUGIN_ID", "opentofu"),
				Tier:           pluginhost.Tier(env("STRATT_OPENTOFU_TIER", "trusted")),
				Source:         types.Source{Kind: "opentofu", Name: env("STRATT_OPENTOFU_SOURCE_NAME", "opentofu"), Endpoint: os.Getenv("STRATT_STATE_BACKEND_URL")},
				// stratt_entities write-back grants (operator-declared, §2.1): the
				// identity schemes / label keys / facet namespaces tofu outputs may
				// project. Empty by default — an ungranted emission is rejected, not
				// silently written (defence-in-depth, ADR-0047 §1).
				IdentitySchemes: splitNonEmpty(os.Getenv("STRATT_OPENTOFU_IDENTITY_SCHEMES")),
				LabelKeys:       splitNonEmpty(os.Getenv("STRATT_OPENTOFU_LABEL_KEYS")),
				FacetNamespaces: splitNonEmpty(os.Getenv("STRATT_OPENTOFU_FACET_NAMESPACES")),
			}
			host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log).UsePlanStore(plans)
			if err := registerPluginActuator("opentofu", host, true, grant, plans); err != nil {
				return err
			}
			log.Info("opentofu plugin actuator registered", "addr", tofuPluginAddr, "backend", os.Getenv("STRATT_STATE_BACKEND_URL"))
		} else {
			// opentofu is a plugin-only Actuator now (the in-tree pod actuator was
			// retired, ADR-0046/0047): the state backend is still served for a peer
			// Cell's plugin, but no actuator is registered here without its address.
			log.Info("opentofu plugin not configured (STRATT_OPENTOFU_PLUGIN_ADDR empty); actuator disabled, state backend still served")
		}
	} else {
		log.Info("opentofu actuator disabled (STRATT_STATE_KEY empty)")
	}

	// ── Crossplane build Actuator over the port (ADR-0059) ───────────────
	// The `builder:` a network Intent names: it applies a Crossplane Claim and
	// projects the built resource back FULLY — existence, identity, labels, AND the
	// net.subnet Facet it just built. NetBox (the IPAM SoR) ALSO knows net.subnet;
	// that is resolved by multi-source Facet ownership (ADR-0060), never by stripping
	// this grant. NetBox and Crossplane now co-own net.subnet (ADR-0060 multi-source):
	// both project it, each its own row, NetBox declared authoritative.
	if addr := os.Getenv("STRATT_CROSSPLANE_PLUGIN_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("crossplane plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		// The Actuator grant: Crossplane BUILDS Claims (Apply/Destroy). Its write-backs
		// are Run-provenance ('' source) — an Actuator is not a Source (ADR-0060). The
		// SYNCER half (Crossplane observing its Claims' as-built state as a registered
		// Source) is wired below in the Syncer section (full-featured dual-verb plugin).
		grant := pluginhost.Grant{
			PluginIdentity:  env("STRATT_CROSSPLANE_PLUGIN_ID", "crossplane"),
			Tier:            pluginhost.Tier(env("STRATT_CROSSPLANE_TIER", "trusted")),
			Source:          types.Source{Kind: "crossplane", Name: env("STRATT_CROSSPLANE_SOURCE_NAME", "crossplane")},
			IdentitySchemes: []string{"crossplane.claim"},
			LabelKeys:       []string{"source", "fleet", "role", "tier"},
			FacetNamespaces: []string{"net.subnet"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		if err := registerPluginActuator("crossplane", host, true, grant, nil); err != nil {
			return err
		}
		// crossplane/provision Action (ADR-0059): the targetless builder an
		// Intent/Subnet launches. Reuses the host/grant; the build projects the subnet
		// Entity + correlation label (entity-only, like awsec2/create-vm), and the
		// Syncer below supplies net.subnet.
		if err := registerPluginAction("crossplane/provision", host, true); err != nil {
			return err
		}
		log.Info("crossplane plugin actuator registered", "addr", addr)
	} else {
		log.Info("no Crossplane plugin configured (STRATT_CROSSPLANE_PLUGIN_ADDR empty); actuator disabled")
	}

	// ── Evidence store (§2.4, ADR-0029) ─────────────────────────────────
	// Gated on STRATT_EVIDENCE_BUCKET: without it, Findings open unsealed (a
	// logged no-op), like the opentofu actuator is gated on a state key.
	// Object-store credentials arrive via the SDK env chain (§2.5 env-stub),
	// reusing the same AWS wiring as the EC2 Syncer.
	var evidence *evidencestore.Store
	if bucket := os.Getenv("STRATT_EVIDENCE_BUCKET"); bucket != "" {
		retentionDays, _ := strconv.Atoi(env("STRATT_EVIDENCE_RETENTION_DAYS", "365"))
		evidence, err = evidencestore.New(ctx, evidencestore.Config{
			// A dedicated endpoint (the object store is a distinct service from
			// the EC2 mock on STRATT_AWS_ENDPOINT); empty falls back to the AWS
			// default resolver (real S3).
			Endpoint:      env("STRATT_EVIDENCE_ENDPOINT", os.Getenv("STRATT_AWS_ENDPOINT")),
			Region:        env("STRATT_EVIDENCE_REGION", env("STRATT_AWS_REGION", "us-east-1")),
			Bucket:        bucket,
			RetentionDays: retentionDays,
			PathStyle:     true,
		})
		if err != nil {
			return err
		}
		if err := evidence.EnsureBucket(ctx); err != nil {
			return fmt.Errorf("evidence store: %w", err)
		}
		log.Info("evidence store ready", "bucket", bucket, "retentionDays", retentionDays)
	} else {
		log.Info("evidence store disabled (STRATT_EVIDENCE_BUCKET empty); findings open unsealed")
	}

	w := worker.New(temporalClient, orchestrate.TaskQueue, worker.Options{})
	w.RegisterWorkflow(orchestrate.RunAgainstView)
	w.RegisterWorkflow(orchestrate.RunAcrossCells)
	w.RegisterWorkflow(orchestrate.RunAction)
	w.RegisterWorkflow(orchestrate.RunDAG)
	w.RegisterWorkflow(orchestrate.RunBaselineCheck)
	// Fenced cross-Cell Source re-home (ADR-0044 slice 7): runs on the Source's
	// home Cell, seals → forwards adopt to the destination → tombstones the old
	// Entities, with a compensating abort before the adopt commits.
	w.RegisterWorkflow(orchestrate.RehomeSourceWorkflow)
	// Peers is the write-side cross-Cell client (ADR-0044 slice 5): it launches
	// and polls child Runs on peer Cells. Nil-safe on a single-Cell estate (no
	// secret ⇒ no peers ⇒ RunAcrossCells is never reached).
	peerClient := cellrouter.NewPeerClient([]byte(os.Getenv("STRATT_CELL_SECRET")))
	// RelayDial tunnels a remote-Site plugin verb over the SAME NATS leaf the site
	// gateway holds (ADR-0049): governance stays hub-side, only the transport
	// lengthens. Keyed by (site, plugin-id).
	relayDial := func(site, pluginID string) siterelay.Dialer {
		return siterelay.NewNATSDialer(siteGateway.Conn(), site, pluginID)
	}
	w.RegisterActivity(&orchestrate.Activities{Store: store, Dispatcher: dispatcher, Bus: bus, Authz: authorizer, Log: log, RelayDial: relayDial, Actuators: registry, Actions: actionRegistry, PluginActions: pluginActions, PluginActuators: pluginActuators, Evidence: evidence, Sites: siteGateway, Peers: peerClient})
	if err := w.Start(); err != nil {
		return fmt.Errorf("temporal worker: %w", err)
	}
	defer w.Stop()
	log.Info("run worker ready", "taskQueue", orchestrate.TaskQueue)

	// Controllers (syncers, reconcilers, engines, the audit sealer, the Salt
	// emitter) are the singleton control loops: collected here and started only
	// on the elected LEADER (HA, ADR-0040), so N replicas don't double-run them.
	// The REST API and the Temporal worker (below) run on EVERY replica.
	// Construction + Register stay inline (idempotent) so config errors fail loud
	// on all replicas; only the Run loops are leader-gated.
	var controllers []func(context.Context)

	// ── Connector home-ownership supervisor (ADR-0045) ───────────────────
	// Each Syncer runs under home-ownership control: a Connector deployed on a
	// Cell that does not yet home its Source STANDS BY (no claim, no external SoR
	// load) and auto-activates when a fenced re-home hands the Source here — no
	// manual redeploy. The DB home gate (migration 00032) is the single-writer
	// backstop underneath; this is the graceful-standby + observability layer. A
	// single-Cell estate (no peers) resolves every Source as greenfield → claims
	// immediately, byte-identical to the pre-ADR-0045 always-run wiring.
	sourceStatus := homegate.NewStatus()
	homeProbe := func(pctx context.Context, endpoint, name string) (string, bool, bool, error) {
		st, body, err := peerClient.Get(pctx, endpoint, "/sources/"+name, "", "system:homegate", authz.KindService)
		if err != nil {
			return "", false, false, err
		}
		if st == http.StatusNotFound {
			return "", false, false, nil // the peer does not home it
		}
		if st != http.StatusOK {
			return "", false, false, fmt.Errorf("peer home probe /sources/%s: HTTP %d", name, st)
		}
		var src struct {
			Cell       string `json:"cell"`
			RehomingTo string `json:"rehomingTo"`
		}
		_ = json.Unmarshal(body, &src)
		return src.Cell, true, src.RehomingTo != "", nil
	}
	homeDeps := homegate.Deps{
		Resolver:              &homegate.Resolver{Cell: cellID, Store: store, Probe: homeProbe},
		Status:                sourceStatus,
		OpenStandbyFinding:    store.WriteHomeStandbyFinding,
		ResolveStandbyFinding: store.ResolveHomeStandbyFinding,
		Log:                   log,
	}
	homeSupervise := func(source string, register, run func(context.Context) error) func(context.Context) {
		return func(cctx context.Context) { homegate.Supervise(cctx, homeDeps, source, register, run) }
	}

	// ── vCenter Syncer plugin over the sovereign port (ADR-0046 Phase B) ──
	// The govmomi content-expertise lives in the stratt-plugin-vcenter binary;
	// the control plane connects to it and GOVERNS what it may write — ownership
	// and the identity-scheme gate come from the operator Grant (finding #1/#4),
	// provenance is stamped core-side (the plugin holds no DB path). The Grant is
	// assembled here from env as the Phase-0 stand-in for a Git/CaC grant.
	if addr := os.Getenv("STRATT_VCENTER_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_VCENTER_SOURCE_NAME", "vcenter-dev")
		interval, err := time.ParseDuration(env("STRATT_VCENTER_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("vcenter interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("vcenter plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:  env("STRATT_VCENTER_PLUGIN_ID", "vcenter"),
			Tier:            pluginhost.Tier(env("STRATT_VCENTER_TIER", "trusted")),
			Source:          types.Source{Kind: "vcenter", Name: sourceName, Endpoint: os.Getenv("STRATT_VCENTER_URL")},
			FacetNamespaces: []string{"vm.config", "vm.runtime", "net.guest", "net.subnet"},
			LabelKeys:       []string{"vcenter.name", "source"},
			// dns.fqdn is a shared cross-source scheme: only honored because the
			// grant lists it AND the tier is trusted (finding #4). vcenter.network.moref
			// identifies vSphere portgroups projected as subnets (ADR-0059).
			IdentitySchemes:  []string{"vcenter.uuid", "vcenter.host.uuid", "dns.fqdn", "vcenter.network.moref"},
			TombstoneSchemes: []string{"vcenter.uuid", "vcenter.host.uuid", "vcenter.network.moref"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no vCenter plugin configured (STRATT_VCENTER_PLUGIN_ADDR empty); syncer idle")
	}

	// ── declared-estate Syncer plugin over the port (ADR-0056 §5) ───────────
	// Devices-as-code: the plugin's system-of-record is a host-list file shipped
	// with the estate (estate/hosts/*.yaml). It projects existence + dns.fqdn
	// identity + labels; the FILE is authoritative and Stratt never writes back
	// (§1.2 — a projection, not a writable CMDB). The Grant honors NO facet (the
	// plugin declares none) and NO tombstone scheme, so a host dropped from the
	// file is never silently deleted (§5). Grant assembled from env here — the
	// Phase-0 stand-in for the sources/ CaC grant (ADR-0056 decisions 1–4).
	if addr := os.Getenv("STRATT_DECLARED_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_DECLARED_SOURCE_NAME", "declared")
		interval, err := time.ParseDuration(env("STRATT_DECLARED_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("declared interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("declared plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_DECLARED_PLUGIN_ID", "declared"),
			Tier:           pluginhost.Tier(env("STRATT_DECLARED_TIER", "trusted")),
			Source:         types.Source{Kind: "declared", Name: sourceName, Endpoint: env("STRATT_DECLARED_PATH", "file:///hosts")},
			LabelKeys:      []string{"os", "role", "tier"},
			// dns.fqdn is a shared cross-source scheme: honored because the grant
			// lists it AND the tier is trusted (finding #4). No FacetNamespaces and
			// no TombstoneSchemes — projection-only, never a silent delete (§5).
			IdentitySchemes: []string{"dns.fqdn"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no declared-estate plugin configured (STRATT_DECLARED_PLUGIN_ADDR empty); syncer idle")
	}

	// ── NetBox topology Syncer over the port (ADR-0059) ──────────────────────
	// NetBox (netbox-community) is the IPAM/DCIM source of truth. The plugin
	// projects `subnet`/`vlan` Entities + the `in-vlan` placement Relation; the
	// grant owns the net.subnet/net.vlan Facet namespaces (owned-but-uncovered —
	// no schema until a Contract consumes them, ADR-0059 M1). Grant from env is
	// the Phase-0 stand-in for a sources/ CaC grant (ADR-0056 1-4).
	if addr := os.Getenv("STRATT_NETBOX_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_NETBOX_SOURCE_NAME", "netbox")
		interval, err := time.ParseDuration(env("STRATT_NETBOX_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("netbox interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("netbox plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:  env("STRATT_NETBOX_PLUGIN_ID", "netbox"),
			Tier:            pluginhost.Tier(env("STRATT_NETBOX_TIER", "trusted")),
			Source:          types.Source{Kind: "netbox", Name: sourceName, Endpoint: os.Getenv("STRATT_NETBOX_URL")},
			FacetNamespaces: []string{"net.subnet", "net.vlan"},
			// NetBox is the IPAM system-of-record: its net.subnet/net.vlan are the
			// declared "truth" (ADR-0060). Crossplane also projects net.subnet (its
			// as-built CIDR — retained signal), but a scalar read resolves to NetBox.
			AuthoritativeFacetNamespaces: []string{"net.subnet", "net.vlan"},
			LabelKeys:                    []string{"source", "net.cidr", "vlan.vid"},
			IdentitySchemes:              []string{"netbox.prefix.id", "netbox.vlan.id"},
			TombstoneSchemes:             []string{"netbox.prefix.id", "netbox.vlan.id"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no NetBox plugin configured (STRATT_NETBOX_PLUGIN_ADDR empty); syncer idle")
	}

	// ── Crossplane Syncer over the port — the SYNCER half of the dual-verb plugin ──
	// (ADR-0060). The Actuator half is registered above; here the SAME plugin Observes
	// its Claims' as-built state back as a REGISTERED Source (resync-able +
	// authority-declarable — the charter-clean path, not a synthesized Actuator source).
	// Co-owns net.subnet but is NOT authoritative: NetBox (the IPAM SoR) is, so a scalar
	// read resolves to NetBox while Crossplane's as-built CIDR is retained as signal.
	if addr := os.Getenv("STRATT_CROSSPLANE_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_CROSSPLANE_SOURCE_NAME", "crossplane")
		interval, err := time.ParseDuration(env("STRATT_CROSSPLANE_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("crossplane interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("crossplane syncer dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:  env("STRATT_CROSSPLANE_PLUGIN_ID", "crossplane"),
			Tier:            pluginhost.Tier(env("STRATT_CROSSPLANE_TIER", "trusted")),
			Source:          types.Source{Kind: "crossplane", Name: sourceName},
			FacetNamespaces: []string{"net.subnet", "net.vlan"}, // co-owner, NOT authoritative (NetBox is)
			// No label ownership: the "source" of a subnet is its PROVENANCE
			// (prov_source_id, ADR-0060), not a shared label key — which is per-key
			// single-owner (ADR-0041) and legitimately held by another Syncer.
			IdentitySchemes:  []string{"crossplane.claim"},
			TombstoneSchemes: []string{"crossplane.claim"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
		log.Info("crossplane plugin syncer registered (dual-verb)", "addr", addr, "interval", interval.String())
	}

	// ── MS Graph device Syncer over the port (ADR-0046/0047 Phase C cutover) ─
	if addr := os.Getenv("STRATT_MSGRAPH_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_MSGRAPH_SOURCE_NAME", "msgraph")
		interval, err := time.ParseDuration(env("STRATT_MSGRAPH_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("msgraph interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("msgraph plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_MSGRAPH_PLUGIN_ID", "msgraph"),
			Tier:             pluginhost.Tier(env("STRATT_MSGRAPH_TIER", "trusted")),
			Source:           types.Source{Kind: "msgraph", Name: sourceName, Endpoint: env("STRATT_MSGRAPH_ENDPOINT", "https://graph.microsoft.com/v1.0")},
			FacetNamespaces:  []string{"device.identity", "device.os", "device.state"},
			LabelKeys:        []string{"graph.name"},
			IdentitySchemes:  []string{"graph.id"},
			TombstoneSchemes: []string{"graph.id"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no MS Graph plugin configured (STRATT_MSGRAPH_PLUGIN_ADDR empty); syncer idle")
	}

	// ── EC2 instance Syncer over the port (Phase C cutover) ──────────────
	// The awsec2 plugin serves BOTH a create-vm build Action and an instance
	// Syncer. The Syncer is OPT-IN (STRATT_AWS_INTERVAL must be set): a
	// build-only deployment (the ADR-0058 provisioning builder) runs the Action
	// without a competing Syncer projection that would re-kind the built instance
	// (the decision-6 kind-unification hazard). Set the interval to enable steady-
	// state observation.
	if awsHost != nil && os.Getenv("STRATT_AWS_INTERVAL") != "" {
		interval, err := time.ParseDuration(os.Getenv("STRATT_AWS_INTERVAL"))
		if err != nil {
			return fmt.Errorf("awsec2 interval: %w", err)
		}
		src := env("STRATT_AWS_SOURCE_NAME", "awsec2")
		controllers = append(controllers, homeSupervise(src, awsHost.Register, func(cctx context.Context) error {
			return awsHost.SyncLoop(cctx, interval)
		}))
	} else if awsHost != nil {
		log.Info("awsec2 plugin: build Action only (STRATT_AWS_INTERVAL unset — Syncer off)")
	} else {
		log.Info("no EC2 plugin configured (STRATT_AWS_PLUGIN_ADDR empty); syncer idle")
	}

	// ── cert-issuer (CLM) Syncer + reconcile Actuator over the port (ADR-0050) ─
	// Both the cert Syncer (Observe) AND the cert lifecycle Actuator (Plan/Apply/
	// Destroy) run over the port on one plugin host; the in-tree pod Action is
	// retired. Edge issuance rides the Site relay (ADR-0049).
	if addr := os.Getenv("STRATT_CLM_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_CLM_SOURCE_NAME", "certissuer")
		interval, err := time.ParseDuration(env("STRATT_CLM_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("certissuer interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("certissuer plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_CLM_PLUGIN_ID", "certissuer"),
			Tier:             pluginhost.Tier(env("STRATT_CLM_TIER", "trusted")),
			Source:           types.Source{Kind: "certissuer", Name: sourceName, Endpoint: os.Getenv("STRATT_CLM_ADDR")},
			FacetNamespaces:  []string{"cert.identity", "cert.expiry"},
			LabelKeys:        []string{"cert.commonName"},
			IdentitySchemes:  []string{"cert.serial"},
			TombstoneSchemes: []string{"cert.serial"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
		// Same host, reconcile Actuator role (ADR-0050): Plan/Apply/Destroy the cert
		// lifecycle. Model Y (no plan-artifact) → no plan store. Dry-runnable.
		if err := registerPluginActuator("certissuer", host, true, grant, nil); err != nil {
			return err
		}
		log.Info("certissuer plugin ready (Syncer + reconcile Actuator)", "addr", addr)
	} else {
		log.Info("no CLM plugin configured (STRATT_CLM_PLUGIN_ADDR empty); cert syncer idle")
	}

	// ── Chef Infra node Syncer over the port (ADR-0046/0047 Phase C cutover) ─
	if addr := os.Getenv("STRATT_CHEF_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_CHEF_SOURCE_NAME", "chef")
		interval, err := time.ParseDuration(env("STRATT_CHEF_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("chef interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("chef plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:  env("STRATT_CHEF_PLUGIN_ID", "chef"),
			Tier:            pluginhost.Tier(env("STRATT_CHEF_TIER", "trusted")),
			Source:          types.Source{Kind: "chef", Name: sourceName, Endpoint: os.Getenv("STRATT_CHEF_SERVER_URL")},
			FacetNamespaces: []string{"chef.node.identity", "chef.node.os", "chef.node.network"},
			// dns.fqdn is a shared cross-source scheme: honored only because it is
			// granted AND the tier is trusted (ADR-0047 finding #4).
			IdentitySchemes:  []string{"chef.node.name", "dns.fqdn"},
			TombstoneSchemes: []string{"chef.node.name"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no Chef plugin configured (STRATT_CHEF_PLUGIN_ADDR empty); syncer idle")
	}

	// ── PuppetDB node Syncer over the port (ADR-0046/0047 Phase C cutover) ───
	if addr := os.Getenv("STRATT_PUPPET_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_PUPPETDB_SOURCE_NAME", "puppet")
		interval, err := time.ParseDuration(env("STRATT_PUPPETDB_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("puppet interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("puppet plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_PUPPET_PLUGIN_ID", "puppet"),
			Tier:             pluginhost.Tier(env("STRATT_PUPPET_TIER", "trusted")),
			Source:           types.Source{Kind: "puppet", Name: sourceName, Endpoint: os.Getenv("STRATT_PUPPETDB_URL")},
			FacetNamespaces:  []string{"puppet.node.identity", "puppet.node.os", "puppet.node.network"},
			IdentitySchemes:  []string{"puppet.certname", "dns.fqdn"},
			TombstoneSchemes: []string{"puppet.certname"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no PuppetDB plugin configured (STRATT_PUPPET_PLUGIN_ADDR empty); syncer idle")
	}

	// ── Salt plugin over the port: grains Syncer + event-bus Emitter ─────
	var saltHost *pluginhost.Host
	if addr := os.Getenv("STRATT_SALT_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_SALT_SOURCE_NAME", "salt")
		interval, err := time.ParseDuration(env("STRATT_SALT_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("salt interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("salt plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_SALT_PLUGIN_ID", "salt"),
			Tier:             pluginhost.Tier(env("STRATT_SALT_TIER", "trusted")),
			Source:           types.Source{Kind: "salt", Name: sourceName, Endpoint: os.Getenv("STRATT_SALT_API_URL")},
			FacetNamespaces:  []string{"salt.node.identity", "salt.node.os", "salt.node.network"},
			IdentitySchemes:  []string{"salt.minion_id", "dns.fqdn"},
			TombstoneSchemes: []string{"salt.minion_id"},
			// The emitter name is grant-bound to this channel identity (anti-spoof).
			EmitterName: env("STRATT_SALT_EMITTER_NAME", sourceName),
		}
		saltHost = pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, saltHost.Register, func(cctx context.Context) error {
			return saltHost.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no Salt plugin configured (STRATT_SALT_PLUGIN_ADDR empty); syncer idle")
	}

	// ── home-ownership collision reconcile (ADR-0045 must-fix 2) ─────────
	// A periodic sweep raising a CRITICAL Finding when >1 Cell homes the same
	// Source name with neither sealed — the greenfield double-writer the slice-2
	// placement check cannot see. Leader-gated; short-circuits on a single-Cell
	// estate (no peers). Never resolves a collision by a silent tiebreak (§2.4).
	controllers = append(controllers, func(cctx context.Context) {
		rec := &homegate.Reconciler{Cell: cellID, Store: store, Probe: homeProbe}
		if err := rec.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("home-collision reconcile stopped", "error", err)
		}
	})

	// ── desired-state reconciliation (§1.2: Git is the declarer) ────────
	if path := os.Getenv("STRATT_DESIRED_STATE_PATH"); path != "" {
		interval, err := time.ParseDuration(env("STRATT_DESIRED_STATE_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("desired-state interval: %w", err)
		}
		maxPrune := 0.0 // 0 → controller default (0.5)
		if v := os.Getenv("STRATT_DESIRED_STATE_MAX_PRUNE"); v != "" {
			if maxPrune, err = strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("desired-state max prune: %w", err)
			}
		}
		maxDelta := 0.0 // 0 → compiler default (0.5)
		if v := os.Getenv("STRATT_INTENT_MAX_DELTA"); v != "" {
			if maxDelta, err = strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("intent max delta: %w", err)
			}
		}
		ctl := &desiredstate.Controller{
			Path: path, Interval: interval, Store: store, Log: log,
			MaxPruneFraction: maxPrune,
			MaxDelta:         maxDelta, CompileStatus: compileStatus,
		}
		controllers = append(controllers, func(cctx context.Context) {
			if err := ctl.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("desired-state controller stopped", "error", err)
			}
		})

		// Authz-home gate (ADR-0044 slice 4): only the authz-home Cell's daemon
		// writes the shared OpenFGA tuple store — else N Cells thrash it. Derived
		// from the CaC Cell set at boot (not the DB, which races the reconcile).
		authzDecls, err := desiredstate.ParseDir(path)
		if err != nil {
			return fmt.Errorf("desired-state parse (authz-home): %w", err)
		}
		authzHome, err := isAuthzHome(cellID, authzDecls.Cells)
		if err != nil {
			return err
		}
		if !authzHome {
			log.Info("not the authz-home Cell; OpenFGA tuple sync is disabled here (a peer Cell owns it)", "cell", cellID)
		}

		// Dispatch-scope reconcile (ADR-0044 slice 6): the NATS subject/stream
		// scope this daemon already applied (env-derived) MUST match its Cell's
		// CaC-declared DispatchPrefix, or hub and agents scope differently. The
		// streams are created but the daemon has not yet begun serving, so a
		// loud-fail here is safe and correct (§2.4).
		if err := reconcileDispatchScope(cellID, scopeTok, authzDecls.Cells); err != nil {
			return err
		}

		// Authz tuples are CaC in the same checkout (§2.5): load now,
		// reload on the reconcile cadence. A failed reload keeps the
		// previous grant set (never silently drop to deny-all mid-flight;
		// never silently gain grants from a broken file either).
		reloadTuples := func() {
			if err := evaluator.LoadTuples(path); err != nil {
				log.Error("authz tuple reload failed; keeping previous grants", "error", err)
				return
			}
			// SCIM group→team membership projects into the tuple union (ADR-0035):
			// the directory owns WHO is in a mapped team; CaC still owns the
			// policy (team→role grants). The §2.1 one-owner guard refuses to
			// project if CaC also declares a mapped team's members — never two
			// writers of one team's membership.
			if memberships, err := store.ProjectedMemberships(ctx); err != nil {
				log.Error("scim projected memberships failed; keeping previous", "error", err)
			} else if mapped, err := store.MappedTeams(ctx); err != nil {
				log.Error("scim mapped teams failed; keeping previous", "error", err)
			} else if team := cacOwnsMappedTeam(evaluator.CACSnapshot(), mapped); team != "" {
				log.Error("scim/CaC two-writer conflict: a mapped team's membership is also declared in CaC; NOT projecting IdP memberships (§2.1)", "team", team)
			} else {
				projected := make([]authz.Tuple, 0, len(memberships))
				for _, m := range memberships {
					projected = append(projected, authz.Tuple{
						User: "principal:" + m.PrincipalID, Relation: authz.RelationMember, Object: "team:" + m.Team,
					})
				}
				evaluator.SetProjectedTuples(projected)
			}
			if fga != nil && authzHome {
				// OpenFGA is a projection of the same Git source (§1.2):
				// desired-state sync, adds and revokes both. ONLY the
				// authz-home Cell writes — the shared store has one writer
				// (ADR-0044 slice 4), else peer Cells would thrash it.
				if err := fga.SyncTuples(ctx, evaluator.Snapshot()); err != nil {
					log.Error("openfga tuple sync failed; server grants may be stale", "error", err)
				}
			}
		}
		reloadTuples()
		// The ongoing reload cadence is leader-only: one writer keeps OpenFGA
		// synced (ADR-0040). Multi-replica deployments must use the OpenFGA
		// server backend — the in-process evaluator is single-replica only.
		controllers = append(controllers, func(cctx context.Context) {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-cctx.Done():
					return
				case <-ticker.C:
					reloadTuples()
				}
			}
		})

		// Declared Triggers project onto Temporal Schedules on the same
		// cadence (§3: Temporal owns all lifecycle; ADR-0010) — Git declares,
		// the graph row is the first projection, the Schedule the second.
		trigReconciler := &triggers.Reconciler{
			Temporal: temporalClient, Store: store, Log: log, Interval: interval,
		}
		controllers = append(controllers, func(cctx context.Context) {
			if err := trigReconciler.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("trigger reconciler stopped", "error", err)
			}
		})

		// Declared Baseline cadences project onto Temporal Schedules the same
		// way (§3: "Baseline cadences"; ADR-0019).
		blReconciler := &baselines.Reconciler{
			Temporal: temporalClient, Store: store, Log: log, Interval: interval,
		}
		controllers = append(controllers, func(cctx context.Context) {
			if err := blReconciler.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("baseline reconciler stopped", "error", err)
			}
		})
	} else {
		log.Info("no desired-state checkout configured (STRATT_DESIRED_STATE_PATH empty); reconciliation off — authz has no tuples (deny-all), triggers idle")
	}

	// ── trigger engine (ADR-0018: Emitter events × CEL → launches) ───────
	engine := &triggerengine.Engine{Store: store, Bus: bus, Temporal: temporalClient, Log: log}
	controllers = append(controllers, func(cctx context.Context) {
		if err := engine.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("trigger engine stopped", "error", err)
		}
	})

	// ── Salt event-bus Emitter over the port (Subscribe verb; ADR-0039) ──
	// Reuses the salt plugin host; the emitter name is grant-bound (anti-spoof),
	// and the Trigger engine CEL-matches the plugin's legible `match` projection,
	// never the opaque payload (ADR-0047 §3). Tag-filtering is the plugin's job.
	if saltHost != nil && env("STRATT_SALT_EVENTS", "false") == "true" {
		controllers = append(controllers, func(cctx context.Context) {
			if err := saltHost.SubscribeLoop(cctx, bus); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("salt emitter stopped", "error", err)
			}
		})
	}

	// ── notifier (ADR-0027: Notices × Subscriptions → webhook delivery) ──
	// The outbound mirror of the trigger engine. Each delivery runs in a pod
	// so the Sink's CredentialRef is injected at spawn (§2.5) — the daemon
	// composes pod specs from pointers, never material.
	notifier := &notify.Dispatcher{Store: store, Bus: bus, Temporal: temporalClient, Authz: authorizer, Log: log}
	controllers = append(controllers, func(cctx context.Context) {
		if err := notifier.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("notifier stopped", "error", err)
		}
	})

	// Audit sealer (ADR-0034): the single writer that chains the append-only
	// audit ledger for tamper-evidence, decoupled from the hot-path append so
	// integrity never bottlenecks the full access log (§1.6, §1.8). Leader-only:
	// two sealers would corrupt the hash chain (ADR-0040).
	controllers = append(controllers, func(cctx context.Context) {
		(&audit.Sealer{Store: store, Log: log}).Run(cctx)
	})

	// Start the controllers: on the elected leader when leader election is on,
	// else directly (single-replica dev/compose). The API + Temporal worker run
	// on every replica regardless (ADR-0040).
	startControllers := func(cctx context.Context) {
		for _, run := range controllers {
			go run(cctx)
		}
	}
	if env("STRATT_LEADER_ELECTION", "false") == "true" {
		// Multi-replica authz MUST use the OpenFGA server backend: the ongoing
		// tuple reload is leader-only, so a non-leader's in-process evaluator
		// would go stale and silently serve wrong grants (§1.6/§1.8). Fail fast
		// rather than hide it — mirroring the OIDC-audience / state-key guards.
		if os.Getenv("STRATT_OPENFGA_URL") == "" {
			return fmt.Errorf("STRATT_LEADER_ELECTION requires STRATT_OPENFGA_URL: multi-replica authorization needs the OpenFGA server backend; the in-process evaluator is single-replica only")
		}
		host, _ := os.Hostname()
		leaderCfg := leader.Config{
			Identity:  env("POD_NAME", host),
			Namespace: env("POD_NAMESPACE", "default"),
			// Cell-scoped lease (ADR-0044): a named Cell's leader must not
			// contend a peer Cell's lease if they share a K8s namespace.
			LeaseName: leaderLeaseName(cellID),
		}
		log.Info("leader election enabled; controllers run on the elected leader", "identity", leaderCfg.Identity)
		go func() {
			if err := leader.Run(ctx, kubeClient, leaderCfg, log, startControllers); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("leader election stopped", "error", err)
			}
		}()
	} else {
		startControllers(ctx)
	}

	// ── interface plane ──────────────────────────────────────────────────
	uiDir := os.Getenv("STRATT_UI_DIR")
	if uiDir != "" {
		log.Info("serving ui", "dir", uiDir)
	}
	server := &api.Server{Store: store, Bus: bus, Temporal: temporalClient, Authz: authorizer, Log: log, CellID: cellID, CellSecret: []byte(os.Getenv("STRATT_CELL_SECRET")), Peers: peerClient, Issuer: oidcIssuer, Audience: oidcAudience, DevPrincipalHeader: devPrincipal, OIDC: oidcResolver, UIDir: uiDir, StateBackend: stateHandler, EmitterIngest: emitters.New(store, bus, log).Handler(), SCIM: scim.New(store, log).Handler(), CompileStatus: compileStatus, Evidence: evidence, SourceStatus: func() map[string]string {
		snap := sourceStatus.Snapshot()
		out := make(map[string]string, len(snap))
		for name, rt := range snap {
			out[name] = string(rt.State)
		}
		return out
	}, SiteLiveness: func(ctx context.Context) (map[string]bool, error) {
		live, err := siteGateway.LiveSites(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(live))
		for name := range live {
			out[name] = true
		}
		return out, nil
	}, SCIMGate: func(ctx context.Context, principalID string) error {
		// Deny a SCIM-managed human the IdP has deactivated (ADR-0035). Unknown
		// to SCIM = not gated. Fail-OPEN on a lookup error: a DB blip must not
		// deny every human (the request would fail at its grant check anyway if
		// the store is truly down) — never a NEW denial from a transient error.
		found, active, err := store.LookupActive(ctx, principalID)
		if err != nil {
			log.Warn("scim deactivation lookup failed; allowing (fail-open)", "principal", principalID, "error", err)
			return nil
		}
		if found && !active {
			return fmt.Errorf("principal %s is deactivated in the identity provider", principalID)
		}
		return nil
	}}
	httpSrv := &http.Server{
		Addr:              env("STRATT_LISTEN_ADDR", ":8080"),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return ctx.Err()
	}
}

// kubeClientset prefers in-cluster config, then KUBECONFIG / ~/.kube/config.
func kubeClientset() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		path := os.Getenv("KUBECONFIG")
		if path == "" {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}

// cacOwnsMappedTeam returns the first team that is BOTH a SCIM-mapping target and
// has its membership declared directly in CaC (a `member` tuple on team:<t>) —
// the §2.1 two-writer conflict. Empty means no conflict.
func cacOwnsMappedTeam(cac []authz.Tuple, mapped map[string]bool) string {
	const prefix = "team:"
	for _, t := range cac {
		if t.Relation != authz.RelationMember {
			continue
		}
		if len(t.Object) <= len(prefix) || t.Object[:len(prefix)] != prefix {
			continue
		}
		if team := t.Object[len(prefix):]; mapped[team] {
			return team
		}
	}
	return ""
}

// tlog adapts slog to Temporal's logger interface.
type tlog struct{ l *slog.Logger }

func (t tlog) Debug(msg string, kv ...any) { t.l.Debug(msg, kv...) }
func (t tlog) Info(msg string, kv ...any)  { t.l.Info(msg, kv...) }
func (t tlog) Warn(msg string, kv ...any)  { t.l.Warn(msg, kv...) }
func (t tlog) Error(msg string, kv ...any) { t.l.Error(msg, kv...) }
