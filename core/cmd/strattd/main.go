// Command strattd is the Stratt control-plane server (charter §3): the
// graph-store frontend, the OpenAPI-first API, the Temporal worker for Run
// Workflows, the K8s Job dispatcher, and the Phase-0 vCenter Syncer.
package main

import (
	"context"
	"encoding/hex"
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
	"github.com/dstout-devops/stratt/core/internal/capability"
	"github.com/dstout-devops/stratt/core/internal/cellrouter"
	"github.com/dstout-devops/stratt/core/internal/compiler"
	"github.com/dstout-devops/stratt/core/internal/connectorregistry"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/cutover"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/emitters"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/evidencestore"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/homegate"
	"github.com/dstout-devops/stratt/core/internal/keycustodian"
	"github.com/dstout-devops/stratt/core/internal/leader"
	"github.com/dstout-devops/stratt/core/internal/notify"
	"github.com/dstout-devops/stratt/core/internal/objectstore"
	"github.com/dstout-devops/stratt/core/internal/observability"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/planstore"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/policy"
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

// version is the build version stamped onto telemetry (OBS-1). Overridable via
// -ldflags "-X main.version=<tag>"; "dev" for an unstamped local build.
var version = "dev"

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

// checkDevPrincipalSafety is the structural auth-bypass guard (enterprise-
// readiness SEC-2). The dev trusted-header resolver forges any Principal from an
// unauthenticated request header — one flag away from a full auth bypass. It
// must never be a deployment-config accident, so boot fails when it is enabled
// alongside a real identity backend (OIDC) — where an attacker would simply omit
// the Bearer and forge X-Stratt-Principal — or in a non-dev environment. Kept a
// pure function so the refusal is unit-tested, not just asserted in review.
func checkDevPrincipalSafety(devPrincipal bool, oidcIssuer, environment string) error {
	if !devPrincipal {
		return nil
	}
	if oidcIssuer != "" {
		return fmt.Errorf("refusing to boot: STRATT_DEV_PRINCIPAL_HEADER=true trusts an unauthenticated header and cannot run alongside STRATT_OIDC_ISSUER (a real identity backend) — an attacker would omit the Bearer and forge X-Stratt-Principal; unset one")
	}
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "production", "prod", "staging":
		return fmt.Errorf("refusing to boot: STRATT_DEV_PRINCIPAL_HEADER=true (trusted-header auth bypass) is forbidden in STRATT_ENVIRONMENT=%q — dev harness only", environment)
	}
	return nil
}

// checkEvidenceConfigured is the WORM-sealing boot probe (enterprise-readiness
// DR-1). The Evidence store (§2.4, ADR-0029) is what makes a Finding's compliance
// bundle tamper-evident and object-locked. It is gated on STRATT_EVIDENCE_BUCKET,
// so a production deploy that simply forgot to set it would run for months
// silently never sealing WORM — a compliance platform's worst failure, and
// invisible. A safe posture is a property of boot, not an operator's memory: in a
// production environment, refuse to boot when no bucket is configured unless the
// operator EXPLICITLY acknowledges unsealed operation (STRATT_EVIDENCE_ALLOW_UNSEALED=true).
func checkEvidenceConfigured(environment, bucket string, allowUnsealed bool) error {
	if bucket != "" || allowUnsealed {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "production", "prod":
		return fmt.Errorf("refusing to boot: STRATT_ENVIRONMENT=%q but STRATT_EVIDENCE_BUCKET is empty — Findings would open UNSEALED and no compliance evidence would ever be object-locked (§2.4). Configure the Evidence store, or explicitly acknowledge unsealed operation with STRATT_EVIDENCE_ALLOW_UNSEALED=true", environment)
	}
	return nil
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

// buildKeyCustodian returns the envelope-encryption custodian for the encrypted stores
// (ADR-0100). The in-core local floor is ALWAYS the default (no external service); when
// STRATT_KEYCUSTODIAN_PROVIDER=openbao-transit AND the openbao plugin is configured, it
// returns a mux whose PRIMARY is the Transit-backed port provider (new writes get a
// KMS-wrapped DEK) while the local floor stays in the set so local-wrapped and legacy
// blobs remain readable (migration + no lock-in). Never required — an unset/absent
// provider is just the floor.
func buildKeyCustodian(stateKey string, log *slog.Logger) (keycustodian.Custodian, error) {
	key, err := hex.DecodeString(stateKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("keycustodian: STRATT_STATE_KEY must be 32 bytes of hex")
	}
	local, err := keycustodian.NewLocal(key)
	if err != nil {
		return nil, err
	}
	if os.Getenv("STRATT_KEYCUSTODIAN_PROVIDER") != "openbao-transit" {
		return local, nil // the floor — the default everywhere incl. dev
	}
	addr := os.Getenv("STRATT_OPENBAO_PLUGIN_ADDR")
	if addr == "" {
		log.Warn("STRATT_KEYCUSTODIAN_PROVIDER=openbao-transit but STRATT_OPENBAO_PLUGIN_ADDR is empty — falling back to the local floor")
		return local, nil
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("keycustodian: openbao plugin dial %s: %w", addr, err)
	}
	// The conn is held for the process lifetime (the custodian is used on every state
	// write/read); it is not deferred-closed here.
	port := keycustodian.NewPort(pluginv1.NewPluginServiceClient(conn), "openbao-transit")
	log.Info("keycustodian: OpenBao Transit provider enabled (in-core floor retained for migration + eject)", "addr", addr)
	return keycustodian.NewMux(port, local), nil
}

func run(ctx context.Context, log *slog.Logger) error {
	// ── graph plane ──────────────────────────────────────────────────────
	dbURL := env("STRATT_DATABASE_URL", "postgres://stratt:stratt-dev@localhost:5432/stratt")
	// Rolling-upgrade schema discipline (UPG-1, ADR-0078):
	//   STRATT_MIGRATE_ONLY  — run migrations and exit 0. The Helm pre-upgrade Job
	//                          uses this so a schema change lands ONCE, in a
	//                          controlled step, before the serving pods roll.
	//   STRATT_SKIP_MIGRATE  — serving replicas skip boot migration when that Job
	//                          owns it, so N replicas never race Up().
	if os.Getenv("STRATT_MIGRATE_ONLY") == "true" {
		if err := graph.MigrateURL(ctx, dbURL); err != nil {
			return err
		}
		log.Info("migrations applied (STRATT_MIGRATE_ONLY); exiting")
		return nil
	}
	var (
		store *graph.Store
		err   error
	)
	if os.Getenv("STRATT_SKIP_MIGRATE") == "true" {
		store, err = graph.ConnectNoMigrate(ctx, dbURL)
		if err != nil {
			return err
		}
		log.Info("graph store ready (migrations owned by the pre-upgrade Job; boot migration skipped)")
	} else {
		store, err = graph.Connect(ctx, dbURL)
		if err != nil {
			return err
		}
		log.Info("graph store ready (migrations applied)")
	}
	defer store.Close()

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
	// ── observability (OBS-1) ────────────────────────────────────────────
	// Providers back /metrics (always) + OTLP traces/metrics (when
	// OTEL_EXPORTER_OTLP_ENDPOINT is set). Stamped with the Cell so multi-region
	// signals are attributable. Never fatal on a missing endpoint (§1.8).
	obs, err := observability.Setup(ctx, observability.Config{
		ServiceName:    "stratt",
		ServiceVersion: version,
		Cell:           cellID,
		OTLPEndpoint:   os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	})
	if err != nil {
		return fmt.Errorf("observability setup: %w", err)
	}
	defer func() {
		shctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if serr := obs.Shutdown(shctx); serr != nil {
			log.Warn("observability shutdown", "error", serr)
		}
	}()
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		log.Info("observability: OTLP export enabled + /metrics", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	} else {
		log.Info("observability: /metrics only (OTEL_EXPORTER_OTLP_ENDPOINT unset; traces are no-op)")
	}
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
	//
	// These are namespaces written back by RUNS rather than projected by a Syncer, so no plugin
	// grant registers them and no Blueprint claims them — the compiler only registers a namespace
	// a route OBSERVES. Owned by the platform team until fact routing is modelled, which is the
	// Phase-2 charter-guardian note os.kernel has carried since the beginning.
	//
	//   * os.kernel      — gathered facts, written back by every converge.
	//   * software.package — "apache is installed at version X". ADR-0080 records this Facet as
	//     having a READER (the patch/advisory check) and no production write-owner, and that gap
	//     is not academic: the apache converge SUCCEEDED on a freshly-built host and the Run then
	//     failed on `facet namespace software.package has no registered owner`. The work was done
	//     and the record of it was refused. A Syncer-owned collector (ADR-0080 slice 2) is the
	//     real answer; until one ships, the team owns it exactly as it owns os.kernel.
	//
	// The LIST now lives in types, because the estate loader has to agree with it: a
	// `facetWriteScope` naming a namespace nothing owns is refused at load
	// (checkFacetWriteScopeOwners), and these are owned by nothing an estate declares. Two copies
	// of the list would make the check and the runtime disagree about what is owned — which is the
	// exact class of split that check exists to catch, arriving one level up.
	for _, ns := range types.TeamOwnedFacetNamespaces() {
		if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
			Namespace: ns, OwnerKind: "team", OwnerRef: "platform",
		}); err != nil {
			return err
		}
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
	oidcIssuersJSON := os.Getenv("STRATT_OIDC_ISSUERS") // multi-issuer (ADR-0101): JSON [{issuer,audience,subNamespace,alias}]
	// Structural auth-bypass guard (enterprise-readiness SEC-2): a safe posture is a
	// property of boot. ANY OIDC config counts as a real backend for the guard.
	oidcMarker := oidcIssuer
	if oidcIssuersJSON != "" {
		oidcMarker = "configured"
	}
	if err := checkDevPrincipalSafety(devPrincipal, oidcMarker, env("STRATT_ENVIRONMENT", "")); err != nil {
		return err
	}
	// Build the issuer set from STRATT_OIDC_ISSUERS (multi) or the single-issuer vars.
	var issuerCfgs []authz.IssuerConfig
	if oidcIssuersJSON != "" {
		if err := json.Unmarshal([]byte(oidcIssuersJSON), &issuerCfgs); err != nil {
			return fmt.Errorf("STRATT_OIDC_ISSUERS: invalid JSON: %w", err)
		}
	} else if oidcIssuer != "" {
		issuerCfgs = []authz.IssuerConfig{{Issuer: oidcIssuer, Audience: oidcAudience, Alias: oidcIssuer}}
	}
	if len(issuerCfgs) > 0 {
		// I-2 (ADR-0101): audience is mandatory per issuer — an issuer without one
		// accepts any token the IdP ever minted for any client. The loud dev opt-out
		// is the only escape.
		allowNoAud := os.Getenv("STRATT_OIDC_ALLOW_NO_AUDIENCE") == "true"
		for i := range issuerCfgs {
			if issuerCfgs[i].Audience == "" {
				if !allowNoAud {
					return fmt.Errorf("OIDC issuer %q has no audience; set it or STRATT_OIDC_ALLOW_NO_AUDIENCE=true (dev only) — I-2", issuerCfgs[i].Issuer)
				}
				log.Warn("OIDC AUDIENCE CHECK DISABLED for issuer — dev harness only", "issuer", issuerCfgs[i].Issuer)
			}
		}
		// Fail fast (I-5): a misconfigured issuer must not boot an API that 401s every
		// Bearer holder while looking healthy; discovery/disjointness are validated here.
		if oidcResolver, err = authz.NewMultiIssuerResolver(ctx, issuerCfgs); err != nil {
			return err
		}
		log.Info("identity backend: oidc", "issuers", len(issuerCfgs))
	} else {
		log.Info("identity backend: none (no OIDC issuer configured); Bearer tokens are not accepted")
	}

	// In-tree Actuator registry (§2.3); out-of-tree Actuators arrive via the
	// plugin Contract surfaces, not this map.
	//
	// ansible (ADR-0051), script + webhook/notify (ADR-0046 Category A) have LEFT the
	// Apache core — ansible/script as EE-Job shims declared as CaC (see below),
	// notify/webhook as a gRPC plugin Action (§1.4 — no `if <tool> {…}` in the spine).
	// Only mcp remains in-tree (registered below) pending its own extraction slice.
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

	// ── Plugin-provided Actuators + Actions over the port (ADR-0047/0048) ────
	// The concurrency-safe routing table shared with the Temporal worker (which runs
	// on every replica) and, at runtime, the Connector/Actuator registry (ADR-0103).
	// It owns the §2.4 exclusive-name check (dup across plugins OR collision with an
	// in-tree Actuator/Action). At boot a collision is fatal — the register* closures
	// propagate the error and crash-loud; the runtime registry downgrades it to a reject.
	plugins := orchestrate.NewPluginRegistry(
		func(name string) bool { _, ok := registry[name]; return ok },       // in-tree Actuator
		func(name string) bool { _, ok := actionRegistry[name]; return ok }, // in-tree Action
	)
	registerPluginAction := func(name string, host *pluginhost.Host, dryRunnable bool) error {
		return plugins.RegisterAction(name, orchestrate.PluginAction{Host: host, DryRunnable: dryRunnable})
	}
	// registerPluginActuator is GONE, and its absence is the ADR-0103 migration's completion
	// receipt: NO Actuator is registered in Go any more. ansible and script went first (ADR-0117
	// k), then cert-issuer, crossplane, mcp, and finally opentofu — whose block was retired rather
	// than migrated, because the estate already declares two opentofu Actuators using the CaC form
	// of its safety property. Every Actuator's grant is now reviewable in Git, which is the whole
	// point: a grant that lives in Go cannot answer "which grant is live?".
	//
	// Actions are NOT yet done — adopt/materialize still registers below, and registerPluginAction
	// survives for it alone.

	// The `ansible` (ADR-0051) and `script` (ADR-0046 Category A) EE-Job Actuators used to
	// be registered here, inline, with their grants hardcoded in Go. They are now CaC
	// declarations — estate/actuators/{ansible,script}.yaml — reconciled into the dispatch
	// table by the connectorregistry on every replica with no strattd restart (ADR-0103,
	// ADR-0117 follow-up k). The blocker was never the transport: it was that a declaration
	// could not express ansible's BOUNDED MF3 facet grant until ADR-0117 D3a added
	// facetNamespaces/identitySchemes to the Actuator Kind. With those fields the
	// declaration is strictly more capable than the boot block was (it can also select the
	// EE image), so the boot block is gone rather than kept as a fallback: two registration
	// paths for one name would collide at §2.4 and make "which grant is live?" unanswerable
	// from Git.
	//
	// Consequence worth stating plainly: a floor that declares neither Actuator has neither.
	// That is the intended CaC posture (Git review authorizes plugin registration, §2.2/§2.3)
	// and it is why the reference estate declares both.

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
			PluginIdentity: env("STRATT_AWS_PLUGIN_ID", "awsec2"),
			Tier:           pluginhost.Tier(env("STRATT_AWS_TIER", "trusted")),
			Source:         types.Source{Kind: "awsec2", Name: env("STRATT_AWS_SOURCE_NAME", "awsec2"), Endpoint: os.Getenv("STRATT_AWS_ENDPOINT")},
			// instance Facets + the resource-graph Facets (ADR-0096). net.subnet is
			// co-owned with crossplane/NetBox — awsec2 is NOT authoritative for it
			// (no AuthoritativeFacetNamespaces entry), so a scalar read resolves to the
			// IPAM SoR while awsec2's as-observed row is retained signal (ADR-0060).
			FacetNamespaces: []string{
				"instance.compute", "instance.network", "instance.state",
				"net.vpc", "net.subnet", "net.securitygroup", "storage.volume",
				// mgmt.address — the OBSERVED reach coordinate (ADR-0143), second substrate.
				// Multi-source alongside `declared` and `vcenter` (ADR-0060); NOT authoritative,
				// for the same reason: these write disjoint Entities, and where they correlate
				// onto one the fail-safe read omits the value and raises a contention Finding
				// rather than picking a winner (§2.4).
				"mgmt.address",
			},
			LabelKeys: []string{"aws.region", "aws.name", "stratt.managed"},
			IdentitySchemes: []string{
				"aws.instanceId", "aws.vpcId", "aws.subnetId", "aws.securityGroupId", "aws.volumeId",
			},
			TombstoneSchemes: []string{
				"aws.instanceId", "aws.vpcId", "aws.subnetId", "aws.securityGroupId", "aws.volumeId",
			},
		}
		awsHost = pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		// The 11 INVOKE Actions (ADR-0095) used to be registered HERE, on the Syncer's host and
		// therefore with the SYNCER's grant. They are now declared:
		// plugins/awsec2/estate/actuators/awsec2.yaml carries them as `actionNames` with an
		// ENTITY-ONLY grant (ADR-0103) — RecordActionResult projects identity + labels and no
		// Facet, so the Syncer's seven Facet namespaces were authority for a path that does not
		// exist. The Syncer half below keeps this grant and stays authoritative for them.
	}

	// awss3 plugin (ADR-0097): bucket lifecycle Actions + a metadata-only bucket
	// Syncer (opt-in via STRATT_AWSS3_INTERVAL, Block B below). One host/grant.
	var s3Host *pluginhost.Host
	if s3Addr := os.Getenv("STRATT_AWSS3_PLUGIN_ADDR"); s3Addr != "" {
		conn, err := grpc.NewClient(s3Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("awss3 plugin dial %s: %w", s3Addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_AWSS3_PLUGIN_ID", "awss3"),
			Tier:             pluginhost.Tier(env("STRATT_AWSS3_TIER", "trusted")),
			Source:           types.Source{Kind: "awss3", Name: env("STRATT_AWSS3_SOURCE_NAME", "awss3"), Endpoint: os.Getenv("STRATT_AWSS3_ENDPOINT")},
			FacetNamespaces:  []string{"bucket.config"},
			LabelKeys:        []string{"aws.region", "stratt.managed"},
			IdentitySchemes:  []string{"aws.bucketArn"},
			TombstoneSchemes: []string{"aws.bucketArn"},
		}
		s3Host = pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		// S3 has no dry-run operation ⇒ every Action is non-DryRunnable.
		// The four bucket-lifecycle Actions are now declared beside the statestore resolve Action
		// (plugins/awss3/estate/actuators/s3-statestore.yaml, ADR-0103), with an entity-only grant.
		// The bucket Syncer below keeps this grant and stays authoritative for bucket.config.
		log.Info("awss3 plugin actions registered", "addr", s3Addr)
	}

	// The notify delivery Actions used to be registered HERE, inline, on
	// STRATT_NOTIFY_PLUGIN_ADDR — one hardcoded `registerPluginAction("notify/webhook")`
	// call that was, quietly, the reason the platform could deliver to exactly one kind of
	// destination. estate/actuators/notify.yaml replaces it (ADR-0125 D3), reconciled by the
	// connectorregistry on every replica with no strattd restart, and it declares BOTH
	// notify/webhook and notify/smtp. Adding a third destination is now a driver plus a name
	// in that file — the boot block is deleted rather than kept as a fallback, because two
	// registration paths for one Action name collide at §2.4 and make "which one is live?"
	// unanswerable from Git (the same call ADR-0117 (k) made for ansible/script).
	//
	// Three undocumented env knobs go with it — STRATT_NOTIFY_{PLUGIN_ADDR,PLUGIN_ID,
	// SOURCE_NAME} — none of which anything in the repo set.

	// adopt/materialize Action is provided by the AWX Connector plugin (ADR-0089), registered
	// in the STRATT_AWX_PLUGIN_ADDR block below — the AWX→CaC transform is tool breadth that
	// lives in the plugin, not a core-owned image. strattd links zero AWX transform code.

	// ── mcp EE-Job transport (ADR-0053) — MIGRATED to the runtime registry (ADR-0103) ──
	// MCP is a generic transport (§1.5), not an in-core protocol: the stratt-mcp shim baked into
	// the EE-mcp image speaks JSON-RPC to the sandboxed server, while the CORE keeps the seam —
	// it resolves the MCPServer declaration + rev, validates call-args against the pin, and pins
	// each rung-3 derived_contract (executeMCP). None of that moved; only the registration did,
	// to plugins/mcp/estate/actuators/mcp.yaml.
	//
	// This block registered `mcp` UNCONDITIONALLY, so every floor had the Actuator whether or not
	// anything used it. Declaring it means a floor that does not admit the plugin estate has no
	// `mcp` — the intended CaC posture and the same trade ansible/script made. Deleted rather than
	// kept as a fallback: two registration paths for one name collide at §2.4 and make "which
	// grant is live?" unanswerable from Git.

	// OpenTofu (ADR-0016): requires the encrypted state backend — without a
	// state key the backend is not mounted and NO plan store exists; a plan-pinned Apply
	// then fails closed at launch rather than degrading to an unpinned one (ADR-0047 §8).
	var stateHandler http.Handler
	// planStore is HOISTED out of the opentofu block so the runtime registry can hand it to any
	// DECLARED plan-capable Actuator (ADR-0103). It used to be built three scopes in, reachable
	// only by the boot block, and connectorregistry.New was passed a literal nil — so a declared
	// Actuator silently had no plan store. For opentofu, the canonical plan-as-artifact Actuator
	// (ADR-0047 §8), that is not a missing nicety: a plan-pinned Apply needs the store the Gate's
	// approved digest lives in, so declaring opentofu while the registry held nil would have
	// dropped plan pinning without a word. Nil stays lawful and means exactly what it meant
	// before — no state key, no plan store, and §8's fail-closed path handles it.
	var planStore *planstore.Store
	if stateKey := os.Getenv("STRATT_STATE_KEY"); stateKey != "" {
		sb, err := statebackend.New(stateKey, store, log)
		if err != nil {
			return err
		}
		stateHandler = sb.Handler()
		// KeyCustodian (ADR-0100): the in-core local floor by default; an OPT-IN mux over
		// an OpenBao Transit provider when STRATT_KEYCUSTODIAN_PROVIDER=openbao-transit
		// (never required — the floor always encrypts state on its own). Same custodian
		// for both encrypted stores.
		custodian, err := buildKeyCustodian(stateKey, log)
		if err != nil {
			return err
		}
		sb.UseCustodian(custodian)
		// The plan store shares the state key — a plan is content-addressed + encrypted
		// (ADR-0047 §8) — and the same custodian as the state backend.
		planStore, err = planstore.New(stateKey, store)
		if err != nil {
			return err
		}
		planStore.UseCustodian(custodian)
		log.Info("state backend + plan store ready (encrypted, ADR-0047 §8)")
	} else {
		log.Info("no state key (STRATT_STATE_KEY empty); state backend not served and no plan store — " +
			"a plan-pinned Apply fails closed rather than running unpinned (ADR-0047 §8)")
	}

	// ── opentofu Actuator (ADR-0046/0047) — RETIRED from boot, not migrated ──────────────
	// The boot block registered an `opentofu` Actuator behind STRATT_OPENTOFU_PLUGIN_ADDR, wired
	// to the CORE-HOSTED encrypted HTTP state backend via STRATT_STATE_BACKEND_URL.
	//
	// It is deleted rather than turned into a declaration, and the distinction matters. The estate
	// already declares two opentofu Actuators — opentofu-network and opentofu-s3 — and both use
	// the CaC form of the same safety property: `requires: [statestore]` holds them PENDING until a
	// verified state provider exists (ADR-0104 D3), so tofu never runs against local plaintext
	// state. Nothing in any estate names the bare `opentofu`, so re-declaring it would mint a
	// dispatch entry with no consumer and a second, differently-backed opentofu surface.
	//
	// What the block guarded is NOT lost. The core-hosted state backend is still served above and
	// still mounted on the API (StateBackend: stateHandler), so a floor that wants that backend
	// points its own declared opentofu Actuator at it through the plugin's TF_HTTP_* config — the
	// plugin's own concern, which is where it always belonged. And the boot precondition's real
	// teeth, "no state key ⇒ no plan store", now live on planStore above and reach every declared
	// Actuator through the registry.

	// ── Crossplane build Actuator over the port (ADR-0059) ───────────────
	// The `builder:` a network Intent names: it applies a Crossplane Claim and
	// projects the built resource back FULLY — existence, identity, labels, AND the
	// net.subnet Facet it just built. NetBox (the IPAM SoR) ALSO knows net.subnet;
	// that is resolved by multi-source Facet ownership (ADR-0060), never by stripping
	// this grant. NetBox and Crossplane now co-own net.subnet (ADR-0060 multi-source):
	// both project it, each its own row, NetBox declared authoritative.
	// ── Crossplane Actuator (ADR-0059/0060) — MIGRATED to the runtime registry (ADR-0103) ──
	// The `crossplane` Actuator and its targetless crossplane/provision Action are now declared
	// (plugins/crossplane/estate/actuators/crossplane.yaml) and dialed + registered at runtime, so
	// they enable/disable with NO strattd restart. The Crossplane SYNCER half stays boot-env below.
	//
	// Removing the block was a FIX, not tidiness. The estate ALREADY declared an Actuator named
	// `crossplane`, so on any floor with STRATT_CROSSPLANE_PLUGIN_ADDR set the two registrations
	// collided at §2.4 — boot ran first and won, so the declaration everyone could read in Git was
	// the one being rejected. "Which grant is live?" was unanswerable, and answered wrongly by
	// anyone who looked at the estate.
	//
	// The declaration carries the FULL grant this block had, including labelKeys, which the Actuator
	// Kind gained for this migration (only the Connector Kind had it, since ADR-0047 §4). Dropping
	// it would have narrowed authority INVISIBLY: an ungranted label key is dropped at the governor,
	// never refused, so the build would have stopped labelling projected subnets in silence.

	// ── Helm Actuator (ADR-0092) — MIGRATED to the runtime registry (ADR-0103) ──
	// helm is now declared as an `Actuator` Kind (estate/actuators/helm.yaml) and dialed +
	// registered at runtime by the connectorregistry (dual-surface: the helm Actuator +
	// the targetless helm/deploy Action), so it enables/disables with NO strattd restart.
	// Its boot-env block (STRATT_HELM_PLUGIN_ADDR) is retired — the first of the 19 boot
	// plugins onto the registry (the other 17 remain env-wired; strangler).

	// ── Evidence store (§2.4, ADR-0029) ─────────────────────────────────
	// Gated on STRATT_EVIDENCE_BUCKET: without it, Findings open unsealed (a
	// logged no-op), like the opentofu actuator is gated on a state key.
	// Object-store credentials arrive via the SDK env chain (§2.5 env-stub),
	// reusing the same AWS wiring as the EC2 Syncer.
	var evidence *evidencestore.Store
	// WORM-sealing boot probe (DR-1): a prod deploy that forgot the bucket must
	// not silently run without ever sealing compliance evidence.
	if err := checkEvidenceConfigured(env("STRATT_ENVIRONMENT", ""),
		os.Getenv("STRATT_EVIDENCE_BUCKET"),
		os.Getenv("STRATT_EVIDENCE_ALLOW_UNSEALED") == "true"); err != nil {
		return err
	}
	if bucket := os.Getenv("STRATT_EVIDENCE_BUCKET"); bucket != "" {
		retentionDays, _ := strconv.Atoi(env("STRATT_EVIDENCE_RETENTION_DAYS", "365"))
		// One shared object-store client (objectstore.ConfigFromEnv resolves the
		// endpoint/region once — canonical STRATT_OBJECTSTORE_*, with the historical
		// STRATT_EVIDENCE_*/STRATT_AWS_* vars honored as fallbacks).
		objClient, oerr := objectstore.New(ctx, objectstore.ConfigFromEnv())
		if oerr != nil {
			return oerr
		}
		evidence, err = evidencestore.New(objClient, bucket, retentionDays)
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
	// Policy Decision Point port (ADR-0072): the built-in CEL provider by default;
	// STRATT_POLICY_EXEC_CMD swaps in an EXTERNAL engine (OPA/Kyverno) over the
	// subprocess transport (ADR-0074); STRATT_POLICY_BYPASS explicitly disables
	// governance (recorded, never silent). Policy is external and
	// swappable/bypassable — never a hardcoded core dependency.
	var decider policy.Decider = policy.CEL{}
	switch {
	case os.Getenv("STRATT_POLICY_BYPASS") == "true":
		decider = policy.Bypass{}
		log.Warn("POLICY BYPASS: the policy decision point is disabled by STRATT_POLICY_BYPASS; all policy checks allow (recorded)")
	case os.Getenv("STRATT_POLICY_EXEC_CMD") != "":
		engine := os.Getenv("STRATT_POLICY_ENGINE")
		if engine == "" {
			engine = "exec"
		}
		cmd := os.Getenv("STRATT_POLICY_EXEC_CMD")
		decider = policy.NewExecCommand(engine, cmd)
		log.Info("policy engine: external subprocess (ADR-0074)", "engine", engine, "cmd", cmd)
	}
	// connReg (the runtime registry, ADR-0103) is built later in the desired-state block; hoisted
	// here so the capability resolver (ADR-0105) can close over it before the worker starts.
	var connReg *connectorregistry.Registry
	acts := &orchestrate.Activities{Store: store, Dispatcher: dispatcher, Bus: bus, Authz: authorizer, Decider: decider, Log: log, RelayDial: relayDial, Actuators: registry, Actions: actionRegistry, Plugins: plugins, Evidence: evidence, Sites: siteGateway, Peers: peerClient}
	// ResolveCapability (ADR-0105): resolve a required capability's bound provider through the
	// runtime registry, read LAZILY so it works regardless of build order — nil-guarded so an
	// Actuator that `requires` a capability before the registry exists fails visibly (§1.8).
	acts.ResolveCapability = func(ctx context.Context, capClass string) (string, error) {
		if connReg == nil {
			return "", fmt.Errorf("capability resolver not ready (runtime registry disabled)")
		}
		return connReg.ResolveCapabilityAction(ctx, capClass)
	}
	// ResolveActuator (ADR-0140 D4): the Actuator-shaped sibling, for a Baseline or Trigger that
	// names a capability class. Same lazy read of connReg, same visible failure when it is absent —
	// a capability-typed reconcile must never fall back to launching against an empty actuator.
	acts.ResolveActuator = func(ctx context.Context, capClass string) (string, error) {
		if connReg == nil {
			return "", fmt.Errorf("capability resolver not ready (runtime registry disabled)")
		}
		return connReg.ResolveCapabilityActuator(ctx, capClass)
	}
	// ResolveBuildWorkflow (ADR-0139 D3): a nested capability Step resolves through the SAME
	// store-backed assembly + pure resolver the compiler uses, exported from desiredstate rather
	// than reimplemented. Two resolvers that can disagree would make the estate mean different
	// things depending on who is asking (§2.4).
	acts.ResolveBuildWorkflow = func(ctx context.Context, capClass, intentKind string) (string, string, error) {
		res, err := desiredstate.ResolveBuildWorkflow(ctx, store, capClass, intentKind)
		if err != nil {
			return "", "", err
		}
		if res.Status != capability.StatusResolved {
			// Fail closed, carrying the resolver's OWN reason: "no verified provider" and "two
			// providers, add a binding" send the reader to different places (§1.8).
			return "", "", fmt.Errorf("%s", res.Reason)
		}
		return res.Provider, res.Workflow, nil
	}
	w.RegisterActivity(acts)
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

	// Connectors that declare a cutover descriptor in their manifest (ADR-0087) collect their
	// clients here; the standing cutover reconciler (registered after the Syncers) reads the
	// descriptors blindly and flags any adopted object still executing at its Source.
	var cutoverClients []cutover.ManifestSource

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
			PluginIdentity: env("STRATT_VCENTER_PLUGIN_ID", "vcenter"),
			Tier:           pluginhost.Tier(env("STRATT_VCENTER_TIER", "trusted")),
			Source:         types.Source{Kind: "vcenter", Name: sourceName, Endpoint: os.Getenv("STRATT_VCENTER_URL")},
			FacetNamespaces: []string{
				"vm.config", "vm.runtime", "net.guest", "net.subnet",
				"storage.datastore", "compute.pool", "net.dvswitch",
				// mgmt.address — the OBSERVED reach coordinate (ADR-0143). The
				// mgmt.address schema has named this writer since ADR-0084 and the grant
				// never carried it, so the projection could not have been written even if
				// the plugin had emitted one: a vSphere VM had no reach coordinate and
				// provision→configure was structurally open.
				//
				// This makes mgmt.address MULTI-SOURCE (declared + vcenter), which
				// ADR-0060 explicitly permits — it dropped the per-namespace lock naming
				// "vSphere and a cloud Syncer would too". NOT declared authoritative:
				// the two write disjoint Entities in practice, and where they DO correlate
				// onto one (a CaC host that is also a vSphere VM), the fail-safe read omits
				// the value and raises a contention Finding rather than picking one (§2.4).
				// That is the correct outcome, so there is nothing to declare here.
				"mgmt.address",
			},
			LabelKeys: []string{"vcenter.name", "source"},
			// dns.fqdn is a shared cross-source scheme: only honored because the
			// grant lists it AND the tier is trusted (finding #4). vcenter.network.moref
			// identifies vSphere portgroups projected as subnets (ADR-0059). Read breadth
			// (ADR-0115) adds region (vcenter.datacenter.moref) + availability-zone
			// (vcenter.cluster.moref) — shared kinds, first projected here.
			IdentitySchemes: []string{
				"vcenter.uuid", "vcenter.host.uuid", "dns.fqdn", "vcenter.network.moref",
				"vcenter.datacenter.moref", "vcenter.cluster.moref", "vcenter.datastore.moref",
				"vcenter.pool.moref", "vcenter.dvs.uuid", "vcenter.folder.moref",
			},
			TombstoneSchemes: []string{
				"vcenter.uuid", "vcenter.host.uuid", "vcenter.network.moref",
				"vcenter.datacenter.moref", "vcenter.cluster.moref", "vcenter.datastore.moref",
				"vcenter.pool.moref", "vcenter.dvs.uuid", "vcenter.folder.moref",
			},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		// The 15 INVOKE Actions (ADR-0113 create-vm/create-portgroup + the ADR-0114 lifecycle set)
		// used to be registered HERE, on the Syncer's host and therefore with the SYNCER's grant.
		// They are now declared: plugins/vcenter/estate/actuators/vcenter.yaml carries them as
		// `actionNames` with the Actuator's OWN, narrower grant (ADR-0103).
		//
		// Two things this buys beyond reviewability. The dispatch surface is verified against the
		// plugin's own Manifest at enable, so a name vcenter does not advertise holds the Actuator
		// back with a diagnostic instead of registering an entry that fails at Invoke. And the
		// Actions stop borrowing the Syncer's write ceiling — an Action's write-back is
		// Run-provenance and entity-shaped, not the Syncer's seven observed Facet namespaces.
		//
		// The Syncer half below keeps this grant and remains the authority for what it observes.
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no vCenter plugin configured (STRATT_VCENTER_PLUGIN_ADDR empty); syncer idle")
	}

	// ── declared-estate Syncer (ADR-0056) — MIGRATED to the runtime registry (ADR-0103) ──
	// declared is now a `Connector` Kind (estate/connectors/declared.yaml) — its Source
	// binding + ownership grant become CaC (what the ADR-0056 "sources/ CaC grant" always
	// wanted) — dialed + home-gated + supervised at runtime by the connectorregistry, so it
	// enables/disables with NO strattd restart. Its boot-env block (STRATT_DECLARED_*) is
	// retired; the FILE stays authoritative (§1.2) and no tombstone scheme means a dropped
	// host is never silently deleted (§5).

	// ── ansible-automation, CONTROLLER half: mirror the AAP estate (ADR-0025 arc, ADR-0127) ──
	// One plugin, TWO Grants, TWO Sources (ADR-0127 D1) — this block is the first. A read-only
	// Syncer projecting an AAP Controller's job templates/workflows/schedules/orgs/teams as
	// `ansible.*` ObservedEntities (§1.2 — AAP stays SoR and keeps executing; the mirror never
	// materializes an executable Stratt Workflow — `stratt adopt` is the deliberate
	// take-authority act, never an import: the projection is always-on, adopt flips an
	// already-observed object to Stratt-managed). The grant owns the five ansible.* Facet +
	// identity schemes (the schedule→template / team→org edges cross by those same owned
	// schemes). Per §2.1 the SOURCE NAME must be distinct per Controller so two Controllers
	// never share a tombstone key — set STRATT_ANSIBLE_AUTOMATION_CONTROLLER_SOURCE_NAME per Controller
	// (default carries the id). The plugin ADDRESS is per Controller too: endpoint/credential
	// are instance config, so each Source is its own instance of the one image (ADR-0127 D1).
	if addr := os.Getenv("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_PLUGIN_ADDR"); addr != "" {
		ctrlID := env("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_ID", "controller")
		sourceName := env("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_SOURCE_NAME", "ansible-controller-"+ctrlID)
		interval, err := time.ParseDuration(env("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("ansible-automation controller interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("ansible-automation controller plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		// ELEVEN owned namespaces (AWX-001/ADR-0154 added ansible.project — the content root a
		// template runs FROM, carrying scm_revision and an ID-joined `uses-project` edge that
		// makes ADR-0085's orphan signal diagnosable instead of merely present; its scm_url is
		// projected with any embedded credential removed, §2.5. AWX-009 added ansible.notification — where AWX sends a job's
		// outcome, name + driver + config KEY NAMES only, because a Slack webhook URL is
		// itself a bearer credential; ADR-0133 added ansible.executionenvironment — a
		// SUPPLY-CHAIN fact; AWX instance groups are deliberately NOT projected, because
		// placement is Sites/Cells and a mirrored one would never govern): ADR-0128 added ansible.credential, ADR-0130 ansible.user,
		// ADR-0132 ansible.label (an ENTITY, because a plugin's label KEYS are a static
		// grant allowlist and an AWX label name is only known at read time).
		// (AWX's LOCAL ACCOUNT table — never identity.subject, which has a single
		// write-owner, and never read by authz: ADR-0079 INV-3).
		// ansible.credential is name+kind only (§2.5) so "which templates use this
		// credential" is a traversal rather than a scan.
		ansibleSchemes := []string{"ansible.template", "ansible.workflow", "ansible.schedule", "ansible.org", "ansible.team", "ansible.credential", "ansible.user", "ansible.label", "ansible.executionenvironment", "ansible.notification", "ansible.project"}
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_PLUGIN_ID", "ansible-automation"),
			Tier:           pluginhost.Tier(env("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_TIER", "trusted")),
			Source:         types.Source{Kind: "ansible.controller", Name: sourceName, Endpoint: os.Getenv("STRATT_ANSIBLE_AUTOMATION_CONTROLLER_ENDPOINT")},
			LabelKeys:      []string{"ansible.name", "ansible.org"},
			// The five ansible.* projection namespaces the Connector owns (its manifest
			// advertises exactly these; registration fails on any mismatch). Each is also
			// the object's identity scheme, and the relation to_schemes (schedules /
			// owned-by / member-of) reference these same owned schemes.
			FacetNamespaces: ansibleSchemes,
			// IdentitySchemes ⊇ FacetNamespaces PLUS ansible.playbook — a POINTABLE-ONLY
			// scheme: the Controller half emits the cross-source `runs` edge onto
			// ansible.playbook (owned by the CONTENT half's Source, never this one) but never
			// writes its facets (not in FacetNamespaces). That cross-source character is the
			// reason the two halves must not share a Source (ADR-0085/0127 D1). An extra
			// IdentityScheme beyond the manifest is legal — Register only requires
			// TombstoneSchemes ⊆ IdentitySchemes and Contracts ⊆ FacetNamespaces.
			IdentitySchemes: append(append([]string{}, ansibleSchemes...), "ansible.playbook"),
		}
		ctrlClient := pluginv1.NewPluginServiceClient(conn)
		host := pluginhost.New(store, ctrlClient, grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
		// The Controller half declares a cutover descriptor in its manifest — collect it for the
		// standing cutover reconciler (ADR-0087; the reconciler reads the descriptor blindly).
		cutoverClients = append(cutoverClients, ctrlClient)
		// The SAME plugin instance also provides the adopt/materialize Action (ADR-0089): the
		// AWX→CaC transform is tool breadth living in the plugin, dispatched by the tool-blind
		// RunAction path over the port (the awsec2 dual-class shape). The plugin resolves the
		// Controller CredentialRef via its own SecretBroker in-pod (§2.5); strattd carries no
		// AWX transform. The content half brokers nothing and advertises no Action.
		if err := registerPluginAction("adopt/materialize", host, false); err != nil {
			return err
		}
		log.Info("adopt/materialize Action registered on the ansible-automation Controller half (ADR-0089)")
	} else {
		log.Info("no ansible-automation Controller half configured (STRATT_ANSIBLE_AUTOMATION_CONTROLLER_PLUGIN_ADDR empty); syncer idle")
	}

	// ── ansible-automation, CONTENT half: "Ansible without AWX" (ADR-0025 arc, ADR-0127) ──
	// The SECOND Grant of the same plugin (ADR-0127 D1) — a separate instance of the one image
	// run with STRATT_ANSIBLE_AUTOMATION_ROLE=content. A read-only Syncer projecting a raw
	// Ansible content root (a mounted Git checkout of playbooks/roles/requirements.yml/
	// inventory) as `ansible.*` ObservedEntities — the PRIMITIVE half of the `ansible` domain
	// the Controller half's orchestration feeds (§1.2 — Git stays SoR; nothing is written back
	// or executed; the mirror never materializes an executable Stratt Workflow — `stratt adopt`
	// is the deliberate take-authority act, never an import: we are always connected and simply
	// know). The grant owns EXACTLY the four ansible.* Facet+identity schemes the plugin
	// populates (§1.1: own what you project) — DISJOINT from the Controller half's five, so the
	// two Sources co-project `ansible.*` with no overlap and no cross-source tombstone
	// (ADR-0042/0060). A SHARED Source would blank half the mirror on every full sync; that is
	// the whole reason one plugin still means two Sources. Per §2.1 the SOURCE NAME is distinct
	// per content root — set STRATT_ANSIBLE_AUTOMATION_CONTENT_SOURCE_NAME per root (default carries the id).
	if addr := os.Getenv("STRATT_ANSIBLE_AUTOMATION_CONTENT_PLUGIN_ADDR"); addr != "" {
		contentID := env("STRATT_ANSIBLE_AUTOMATION_CONTENT_ID", "ansible")
		sourceName := env("STRATT_ANSIBLE_AUTOMATION_CONTENT_SOURCE_NAME", "ansible-content-"+contentID)
		interval, err := time.ParseDuration(env("STRATT_ANSIBLE_AUTOMATION_CONTENT_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("ansible-automation content interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("ansible-automation content plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		// The SEVEN ansible.* projection namespaces this Connector owns (its manifest advertises
		// exactly these; registration fails on any mismatch). Each is also the artifact's
		// identity scheme — including the `depends-on` relation's TO scheme, which is
		// ansible.role, a namespace this same Source already owns, so no cross-source pointable
		// grant is needed (unlike the controller half's ansible.playbook).
		//
		// ansible.varscope is the group_vars/host_vars binding site (ANS-003): scope and KEY
		// NAMES, never values — a vars file routinely holds credentials in the clear, and a
		// vaulted one is projected as present-and-vaulted rather than decrypted (§2.5).
		//
		// ansible.config is the root's ansible.cfg (ANS-005) — the file that changes the meaning
		// of everything else in the root, projected as allowlisted VALUES plus the NAMES of
		// every other key, because a [galaxy_server.*] section holds a real API token (§2.5).
		// ansible.plugin is the repo's own modules and plugins (ANS-006), name/type/path and
		// never contents.
		primitiveSchemes := []string{"ansible.playbook", "ansible.role", "ansible.collection", "ansible.inventory",
			"ansible.varscope", "ansible.config", "ansible.plugin"}
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_ANSIBLE_AUTOMATION_CONTENT_PLUGIN_ID", "ansible-automation"),
			Tier:           pluginhost.Tier(env("STRATT_ANSIBLE_AUTOMATION_CONTENT_TIER", "trusted")),
			Source:         types.Source{Kind: "ansible.content", Name: sourceName},
			// DISJOINT from the Controller half's label keys, and that is load-bearing: a
			// label key has exactly ONE owner (ADR-0041), so when both halves claimed
			// `ansible.name` the second one to register failed outright. Found by the
			// two-Sources integration test; it predates ADR-0127 (the old awx and
			// ansibleproject grants collided identically) and was never caught because
			// nothing had ever run both halves.
			LabelKeys:       []string{"ansible.artifact", "ansible.project"},
			FacetNamespaces: primitiveSchemes,
			IdentitySchemes: primitiveSchemes,
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no ansible-automation content half configured (STRATT_ANSIBLE_AUTOMATION_CONTENT_PLUGIN_ADDR empty); syncer idle")
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

	// ── kubecompute Syncer over the port — the OBSERVE half of the builder (ADR-0151) ──
	// The Actuator half BUILDS a host; this half says how to REACH it. They are separate on
	// purpose: a pod has no address until it is scheduled, so a builder that returned one would be
	// asserting a fact it cannot know (§1.2). The address is observed, on a cadence, after the fact.
	//
	// Without this the build succeeds, the Entity is projected from the Action's write-back — and
	// mgmt.address never appears, so nothing can converge the host that was just built. That is the
	// shape ADR-0142 D4 predicted for a K8s Compute provider: it projects the coordinate it CAUSED,
	// which is the observed producer again.
	if addr := os.Getenv("STRATT_KUBECOMPUTE_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_KUBECOMPUTE_SOURCE_NAME", "kubecompute")
		interval, err := time.ParseDuration(env("STRATT_KUBECOMPUTE_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("kubecompute interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("kubecompute syncer dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity: env("STRATT_KUBECOMPUTE_PLUGIN_ID", "kubecompute"),
			Tier:           pluginhost.Tier(env("STRATT_KUBECOMPUTE_TIER", "trusted")),
			Source:         types.Source{Kind: "kubecompute", Name: sourceName},
			// ONE Facet namespace, and it is the reach coordinate. A builder that cannot say how to
			// reach what it built has not finished the job — but it gets to say nothing else.
			FacetNamespaces: []string{"mgmt.address"},
			// AUTHORITATIVE for the hosts it built: it caused the address, so nothing else has a
			// better claim on it (ADR-0060's declared-authority path, and ADR-0142 D4's
			// "observed or caused, never computed").
			AuthoritativeFacetNamespaces: []string{"mgmt.address"},
			// The CORRELATION label the provisioning reconcile reads to see the instance as built
			// (ADR-0120), plus the fleet key a View selects on. NOT `stratt.managed`: a label key has
			// exactly ONE owner (ADR-0041) and that one is already claimed elsewhere, so asking for
			// it fails the whole registration and the Syncer never starts — costing the reach
			// coordinate for a marker this plugin does not need to own.
			LabelKeys:        []string{"stratt.intent/instance", "fleet"},
			IdentitySchemes:  []string{"kube.host"},
			TombstoneSchemes: []string{"kube.host"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
	} else {
		log.Info("no kubecompute plugin configured (STRATT_KUBECOMPUTE_PLUGIN_ADDR empty); syncer idle")
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

	// ── awss3 bucket Syncer over the port (ADR-0097) — OPT-IN (STRATT_AWSS3_INTERVAL).
	// Metadata-only: bucket existence/config, NEVER object bytes (§1.2).
	if s3Host != nil && os.Getenv("STRATT_AWSS3_INTERVAL") != "" {
		interval, err := time.ParseDuration(os.Getenv("STRATT_AWSS3_INTERVAL"))
		if err != nil {
			return fmt.Errorf("awss3 interval: %w", err)
		}
		src := env("STRATT_AWSS3_SOURCE_NAME", "awss3")
		controllers = append(controllers, homeSupervise(src, s3Host.Register, func(cctx context.Context) error {
			return s3Host.SyncLoop(cctx, interval)
		}))
	} else if s3Host != nil {
		log.Info("awss3 plugin: Actions only (STRATT_AWSS3_INTERVAL unset — Syncer off)")
	}

	// ── openbao plugin: cert-issuer Syncer + reconcile Actuator over the port ─────
	// (ADR-0050/0098). Both the cert Syncer (Observe) AND the cert lifecycle Actuator
	// (Plan/Apply/Destroy) run over the port on one plugin host; the in-tree pod Action
	// is retired. The plugin is tool-named `openbao` (its backend); the Actuator is the
	// NEUTRAL `cert-issuer` (§1.5 — a step-ca plugin could implement it). Edge issuance
	// rides the Site relay (ADR-0049).
	if addr := os.Getenv("STRATT_OPENBAO_PLUGIN_ADDR"); addr != "" {
		sourceName := env("STRATT_OPENBAO_SOURCE_NAME", "openbao")
		interval, err := time.ParseDuration(env("STRATT_OPENBAO_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("openbao interval: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("openbao plugin dial %s: %w", addr, err)
		}
		defer conn.Close()
		grant := pluginhost.Grant{
			PluginIdentity:   env("STRATT_OPENBAO_PLUGIN_ID", "openbao"),
			Tier:             pluginhost.Tier(env("STRATT_OPENBAO_TIER", "trusted")),
			Source:           types.Source{Kind: "openbao", Name: sourceName, Endpoint: os.Getenv("STRATT_OPENBAO_ADDR")},
			FacetNamespaces:  []string{"cert.identity", "cert.expiry", "identity.credential", "ca.config", "kv.metadata"},
			LabelKeys:        []string{"cert.commonName", "kv.mount"},
			IdentitySchemes:  []string{"cert.serial", "pki.caSerial", "kv.path"},
			TombstoneSchemes: []string{"cert.serial", "pki.caSerial", "kv.path"},
		}
		host := pluginhost.New(store, pluginv1.NewPluginServiceClient(conn), grant, log)
		controllers = append(controllers, homeSupervise(sourceName, host.Register, func(cctx context.Context) error {
			return host.SyncLoop(cctx, interval)
		}))
		// The cert-issuer reconcile Actuator (ADR-0050) and the administrative PKI Actions
		// (ADR-0098 E2) used to be registered HERE, with the grant above — the same grant the
		// Syncer uses, because one Go value served both roles. They are now a CaC declaration:
		// plugins/openbao/estate/actuators/cert-issuer.yaml, reconciled into the dispatch table
		// by the connectorregistry on every replica with no strattd restart (ADR-0103).
		//
		// The blocker was never the transport. It was that ADR-0140 D4 cannot capability-type the
		// cert reconcile while the declared `certissuer` provider (`openbao`) and the Actuator
		// actually serving it (`cert-issuer`) are different objects — resolution lands on a
		// declaration with an empty facet grant and the reconcile's write-back silently vanishes.
		// The declaration is also strictly more capable than this block was: it carries its own
		// NARROWER grant (cert.identity + cert.expiry, not the Syncer's five), which is the least
		// authority the Actuator actually needs.
		//
		// Deleted rather than kept as a fallback, for the reason the ansible/script migration
		// records: two registration paths for one name collide at §2.4 and make "which grant is
		// live?" unanswerable from Git. A floor that declares no cert-issuer has none — the
		// intended CaC posture, and why the reference estate declares it.
		log.Info("openbao plugin ready (cert Syncer; cert-issuer Actuator + PKI Actions are CaC)", "addr", addr)
	} else {
		log.Info("no openbao plugin configured (STRATT_OPENBAO_PLUGIN_ADDR empty); cert syncer idle")
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

	// ── standing cutover reconcile (ADR-0087) ───────────────────────────
	// Continuously flags an adopted object still executing at its Source: a Stratt Workflow's
	// adoptedFrom whose foreign executor (an enabled AWX schedule) is still live becomes a
	// Finding, so it never runs in both places (§7.6/§1.8). Tool-blind — the relation/facet to
	// check comes from each Connector's manifest cutover descriptor. Idle when none declares one.
	if len(cutoverClients) > 0 {
		cutoverInterval, err := time.ParseDuration(env("STRATT_CUTOVER_INTERVAL", "5m"))
		if err != nil {
			return fmt.Errorf("cutover interval: %w", err)
		}
		controllers = append(controllers, func(cctx context.Context) {
			rec := &cutover.Reconciler{Store: store, Clients: cutoverClients, Interval: cutoverInterval, Log: log}
			if err := rec.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("cutover reconcile stopped", "error", err)
			}
		})
	}

	// admissionControls is the estate's admission policy, snapshotted at boot so
	// the API's imperative door (POST /desired-state/*, PUT /views/*) admits
	// through the same PDP port the Git reconcile uses (enterprise-readiness
	// GOV-2). Empty when no estate is configured.
	var admissionControls []types.Control
	// ── desired-state reconciliation (§1.2: Git is the declarer) ────────
	// The runtime Connector/Actuator registry (ADR-0103) is built inside the desired-state
	// block below (it needs the reconcile cadence); connReg is hoisted up by the worker setup so
	// the API server can read its per-declaration status (D6) and the capability resolver can close
	// over it. Nil when reconciliation is off.
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
			Decider: decider,
		}
		controllers = append(controllers, func(cctx context.Context) {
			if err := ctl.Run(cctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("desired-state controller stopped", "error", err)
			}
		})

		// Runtime Connector/Actuator registry (ADR-0103): reconcile the declared
		// Connector/Actuator rows (populated by the desired-state loop above) into dialed +
		// registered plugins with NO restart. Actuators reconcile on EVERY replica (their
		// dispatch-map membership must be visible to any worker — D3); Connector Syncers
		// reconcile LEADER-ONLY (graph writers, home-gated). helm's planstore is nil (as its
		// boot-env block was); an Actuator that needs one is a later-slice concern.
		// planStore (nil when the floor has no state key) reaches every declared Actuator here —
		// the "later-slice concern" the previous nil booked (ADR-0047 §8).
		connReg = connectorregistry.New(store, plugins, homeDeps, planStore,
			func(addr string) (*grpc.ClientConn, error) {
				return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			},
			interval, log)
		go connReg.RunActuators(ctx)                                       // every replica
		controllers = append(controllers, connReg.RunConnectors)           // leader-only
		controllers = append(controllers, connReg.RunProviderVerification) // leader-only (ADR-0104 D1)

		// Authz-home gate (ADR-0044 slice 4): only the authz-home Cell's daemon
		// writes the shared OpenFGA tuple store — else N Cells thrash it. Derived
		// from the CaC Cell set at boot (not the DB, which races the reconcile).
		authzDecls, err := desiredstate.ParseDir(path, nil)
		if err != nil {
			return fmt.Errorf("desired-state parse (authz-home): %w", err)
		}
		// That parse also registered every admitted plugin's SELF contracts (ADR-0138 D3/D4). Pin
		// them exactly as the shipped ones were pinned above, so drift against a registered pin
		// stays blocking — D4's "core stops embedding and instead pins at registration".
		//
		// This runs on EVERY replica and BEFORE the API handler is built, and both matter. The
		// desired-state controller is leader-only, so registering there would give the leader a
		// contract set its followers lack while the Temporal worker validates action params on
		// every replica — the ADR-0103 D3 routing hazard in the validation layer. And
		// contract.Fingerprint() is captured once inside api.Server.Handler(), so a later
		// registration would ship peers a stale federation stamp.
		if estateContracts := contract.EstateContracts(); len(estateContracts) > 0 {
			for _, c := range estateContracts {
				if err := store.RegisterContract(ctx, c); err != nil {
					return err
				}
			}
			log.Info("plugin self contracts pinned (ADR-0138 D4)", "count", len(estateContracts))
		}
		// Snapshot the estate's admission policy for the API's imperative door
		// (GOV-2). Parsed with a nil decider above (this load only reads Cells +
		// admission controls; the reconcile controller admits live).
		admissionControls = authzDecls.AdmissionControls
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
		// Register the identity projector as the single §2.1 write-owner of
		// identity.subject + identity.name before the reconcile projects (ADR-0079
		// slice 3). Best-effort + loud (§1.8): an owner conflict skips identity
		// projection but never crashes the daemon.
		if err := store.EnsureIdentitySubjectOwner(ctx); err != nil {
			log.Error("identity.subject owner registration failed; SCIM identity projection disabled", "error", err)
		}
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
			// SCIM identities → graph (ADR-0079 slice 3): project users/groups as
			// Entities carrying identity.subject + member-of Relations, on the same
			// SCIM reconcile cadence. Best-effort — a failure keeps the previous
			// projection (§1.8, logged). Leader-only (this closure runs on the leader).
			if err := store.ProjectSCIMEntities(ctx); err != nil {
				log.Error("scim identity projection failed; keeping previous", "error", err)
			}
			// Identity correlation (ADR-0079 slice 4a): link credentials to the
			// subjects they attest (`identifies`) and raise the leaver-credential
			// Finding — a cross-source (PKI × IdP) signal no island model can see.
			// Runs after the subject/credential projections so both exist. Best-effort.
			if err := store.CorrelateIdentities(ctx); err != nil {
				log.Error("identity correlation failed; keeping previous", "error", err)
			}
			// Software-advisory check (ADR-0080 slice 3): load the declarable advisory
			// ruleset from the estate (compliance-as-data) and raise patch/advisory
			// Findings across the WHOLE software dimension — packages, container
			// images, charts — in one form-agnostic pass. Best-effort — a broken
			// ruleset keeps the previous (§1.8).
			if advisories, err := desiredstate.LoadSoftwareAdvisories(path); err != nil {
				log.Error("load software advisories failed; keeping previous", "error", err)
			} else if err := store.CheckSoftwareAdvisories(ctx, advisories); err != nil {
				log.Error("software advisory check failed; keeping previous", "error", err)
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
	engine := &triggerengine.Engine{
		Store: store, Bus: bus, Temporal: temporalClient, Log: log,
		// A schedule/event Trigger naming a capability resolves through the same registry the
		// Baseline path uses (ADR-0140 D4) — one resolution, one binding, one audit (§1.6).
		ResolveActuator: acts.ResolveActuator,
	}
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
	server := &api.Server{Store: store, Bus: bus, Temporal: temporalClient, Authz: authorizer, Log: log, CellID: cellID, CellSecret: []byte(os.Getenv("STRATT_CELL_SECRET")), Peers: peerClient, Issuer: oidcIssuer, Audience: oidcAudience, DevPrincipalHeader: devPrincipal, OIDC: oidcResolver, UIDir: uiDir, StateBackend: stateHandler, EmitterIngest: emitters.New(store, bus, log).Handler(), SCIM: scim.New(store, log).Handler(), CompileStatus: compileStatus, Evidence: evidence, Decider: decider, AdmissionControls: admissionControls, Metrics: obs.MetricsHandler(), SourceStatus: func() map[string]string {
		snap := sourceStatus.Snapshot()
		out := make(map[string]string, len(snap))
		for name, rt := range snap {
			out[name] = string(rt.State)
		}
		return out
	}, PluginStatus: func() map[string]api.PluginRuntimeStatus {
		if connReg == nil {
			return nil
		}
		snap := connReg.Statuses()
		out := make(map[string]api.PluginRuntimeStatus, len(snap))
		for k, st := range snap {
			v := api.PluginRuntimeStatus{Enabled: st.Enabled}
			if st.Error != "" {
				e := st.Error
				v.Error = &e
			}
			out[k] = v
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
