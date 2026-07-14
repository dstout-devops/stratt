// Command strattd is the Stratt control-plane server (charter §3): the
// graph-store frontend, the OpenAPI-first API, the Temporal worker for Run
// Workflows, the K8s Job dispatcher, and the Phase-0 vCenter Syncer.
package main

import (
	"context"
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
	awsaction "github.com/dstout-devops/stratt/core/internal/actions/awsec2"
	certaction "github.com/dstout-devops/stratt/core/internal/actions/certissuer"
	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/actuators/ansible"
	mcpact "github.com/dstout-devops/stratt/core/internal/actuators/mcp"
	"github.com/dstout-devops/stratt/core/internal/actuators/opentofu"
	"github.com/dstout-devops/stratt/core/internal/actuators/script"
	"github.com/dstout-devops/stratt/core/internal/actuators/webhook"
	"github.com/dstout-devops/stratt/core/internal/api"
	"github.com/dstout-devops/stratt/core/internal/audit"
	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/baselines"
	"github.com/dstout-devops/stratt/core/internal/compiler"
	"github.com/dstout-devops/stratt/core/internal/connectors/awsec2"
	certsyncer "github.com/dstout-devops/stratt/core/internal/connectors/certissuer"
	"github.com/dstout-devops/stratt/core/internal/connectors/chef"
	"github.com/dstout-devops/stratt/core/internal/connectors/msgraph"
	"github.com/dstout-devops/stratt/core/internal/connectors/puppet"
	"github.com/dstout-devops/stratt/core/internal/connectors/salt"
	"github.com/dstout-devops/stratt/core/internal/connectors/vcenter"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/emitters"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/evidencestore"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/notify"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/scim"
	"github.com/dstout-devops/stratt/core/internal/sitegw"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
	"github.com/dstout-devops/stratt/core/internal/statebackend"
	"github.com/dstout-devops/stratt/core/internal/triggerengine"
	"github.com/dstout-devops/stratt/core/internal/triggers"
	"github.com/dstout-devops/stratt/types"
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
	bus, err := events.Connect(ctx, env("STRATT_NATS_URL", "nats://localhost:4222"))
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
	log.Info("event bus ready", "stream", events.StreamName)

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
		Namespace: env("STRATT_TEMPORAL_NAMESPACE", "default"),
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
	if issuer := os.Getenv("STRATT_OIDC_ISSUER"); issuer != "" {
		// Production guard (ADR-0013, slice-5 guardian flag): an issuer
		// without an audience accepts any token the IdP ever minted for any
		// client. Skipping the audience check is a loud, explicit dev-only
		// opt-out — never a default.
		audience := os.Getenv("STRATT_OIDC_AUDIENCE")
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
	registry := map[string]actuators.Actuator{}
	for _, a := range []actuators.Actuator{ansible.Actuator{}, script.Actuator{}, webhook.Actuator{}} {
		registry[a.Name()] = a
	}

	// In-tree Action registry (§2.2, ADR-0031): targetless typed operations
	// shipped by Connectors — the write side of cert-issuer (retiring the
	// ADR-0030 Actuator-in-disguise) and awsec2 create-vm.
	actionRegistry := actions.Registry{}
	for _, act := range []actions.Action{
		certaction.Issue(), certaction.Renew(), certaction.Revoke(),
		awsaction.CreateVM(env("STRATT_EE_ACTIONS_IMAGE", "stratt-ee-actions:dev")),
	} {
		actionRegistry[act.Name()] = act
	}
	log.Info("action registry ready", "actions", len(actionRegistry))

	// mcp Actuator (ADR-0022): store-backed declaration + pin lookups; the
	// external server runs only inside the sandboxed EE pod.
	mcpActuator := mcpact.FromEnv(store.GetMCPServer,
		func(ctx context.Context, name string, version int) (types.Contract, bool, error) {
			c, err := store.GetContract(ctx, name, version)
			if errors.Is(err, graph.ErrNotFound) {
				return types.Contract{}, false, nil
			}
			if err != nil {
				return types.Contract{}, false, err
			}
			return c, true, nil
		})
	registry[mcpActuator.Name()] = mcpActuator
	log.Info("mcp actuator ready", "eeImage", mcpActuator.DefaultImage)

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
		tofuActuator := opentofu.FromEnv(sb.WorkspaceCredential)
		if tofuActuator.BackendURL == "" {
			return fmt.Errorf("STRATT_STATE_KEY is set but STRATT_STATE_BACKEND_URL is empty — execution pods need the backend address (ADR-0016)")
		}
		registry[tofuActuator.Name()] = tofuActuator
		log.Info("opentofu actuator ready", "backend", tofuActuator.BackendURL, "eeImage", tofuActuator.DefaultImage)
	} else {
		log.Info("opentofu actuator disabled (STRATT_STATE_KEY empty)")
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
	w.RegisterWorkflow(orchestrate.RunAction)
	w.RegisterWorkflow(orchestrate.RunDAG)
	w.RegisterWorkflow(orchestrate.RunBaselineCheck)
	w.RegisterActivity(&orchestrate.Activities{Store: store, Dispatcher: dispatcher, Bus: bus, Authz: authorizer, Actuators: registry, Actions: actionRegistry, Evidence: evidence, Sites: siteGateway})
	if err := w.Start(); err != nil {
		return fmt.Errorf("temporal worker: %w", err)
	}
	defer w.Stop()
	log.Info("run worker ready", "taskQueue", orchestrate.TaskQueue)

	// ── Phase-0 vCenter Syncer (started when a Source is configured) ─────
	if endpoint := os.Getenv("STRATT_VCENTER_URL"); endpoint != "" {
		syncer := vcenter.NewSyncer(vcenter.Config{
			Endpoint: endpoint,
			// Credentials via env is the Phase-0 CredentialRef injection
			// stub; material is never persisted (§2.5).
			Username:   env("STRATT_VCENTER_USERNAME", "user"),
			Password:   env("STRATT_VCENTER_PASSWORD", "pass"),
			Insecure:   env("STRATT_VCENTER_INSECURE", "false") == "true",
			SourceName: env("STRATT_VCENTER_SOURCE_NAME", "vcenter-dev"),
		}, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("vcenter syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no vCenter Source configured (STRATT_VCENTER_URL empty); syncer idle")
	}

	// ── MS Graph Syncer (ADR-0014; started when a Source is configured) ──
	if tenant := os.Getenv("STRATT_MSGRAPH_TENANT_ID"); tenant != "" {
		interval, err := time.ParseDuration(env("STRATT_MSGRAPH_INTERVAL", "30s"))
		if err != nil {
			return fmt.Errorf("msgraph interval: %w", err)
		}
		syncer := msgraph.NewSyncer(msgraph.Config{
			Endpoint: env("STRATT_MSGRAPH_ENDPOINT", "https://graph.microsoft.com/v1.0"),
			TenantID: tenant,
			ClientID: os.Getenv("STRATT_MSGRAPH_CLIENT_ID"),
			// Env credential stub, same posture as vCenter (§2.5: material
			// never persists; CredentialRef brokering for Syncers is the
			// recorded follow-up).
			ClientSecret: os.Getenv("STRATT_MSGRAPH_CLIENT_SECRET"),
			TokenURL:     os.Getenv("STRATT_MSGRAPH_TOKEN_URL"),
			SourceName:   env("STRATT_MSGRAPH_SOURCE_NAME", "msgraph"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("msgraph syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no MS Graph Source configured (STRATT_MSGRAPH_TENANT_ID empty); syncer idle")
	}

	// ── EC2 cloud-instance Syncer (ADR-0014) ─────────────────────────────
	if region := os.Getenv("STRATT_AWS_REGION"); region != "" {
		interval, err := time.ParseDuration(env("STRATT_AWS_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("awsec2 interval: %w", err)
		}
		syncer := awsec2.NewSyncer(awsec2.Config{
			// Endpoint override points at the moto stand-in in dev;
			// credentials arrive via the SDK's standard env chain (§2.5
			// env-stub posture, CredentialRef brokering is the follow-up).
			Endpoint:   os.Getenv("STRATT_AWS_ENDPOINT"),
			Region:     region,
			SourceName: env("STRATT_AWS_SOURCE_NAME", "awsec2"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("awsec2 syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no EC2 Source configured (STRATT_AWS_REGION empty); syncer idle")
	}

	// ── cert-issuer (CLM) Syncer (ADR-0030; started when a Source is set) ─
	if addr := os.Getenv("STRATT_CLM_ADDR"); addr != "" {
		interval, err := time.ParseDuration(env("STRATT_CLM_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("certissuer interval: %w", err)
		}
		syncer := certsyncer.NewSyncer(certsyncer.Config{
			// Read-side projection credential via the env chain (§2.5); the
			// write side (issue/revoke) injects its token into the EE pod.
			Addr:       addr,
			Token:      os.Getenv("STRATT_CLM_TOKEN"),
			Mount:      env("STRATT_CLM_MOUNT", "pki"),
			SourceName: env("STRATT_CLM_SOURCE_NAME", "certissuer"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("certissuer syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no CLM Source configured (STRATT_CLM_ADDR empty); cert syncer idle")
	}

	// ── Chef Infra Server node Syncer (ADR-0037; config-mgmt SoR ingest) ─
	if serverURL := os.Getenv("STRATT_CHEF_SERVER_URL"); serverURL != "" {
		interval, err := time.ParseDuration(env("STRATT_CHEF_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("chef interval: %w", err)
		}
		// The signing key is read from a mounted PEM file (§2.5: material
		// stays a file the process reads, never persisted to the graph);
		// STRATT_CHEF_KEY may carry inline PEM for dev.
		keyPEM := os.Getenv("STRATT_CHEF_KEY")
		if keyFile := os.Getenv("STRATT_CHEF_KEY_FILE"); keyFile != "" {
			b, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("chef key file: %w", err)
			}
			keyPEM = string(b)
		}
		skipSSL := env("STRATT_CHEF_SKIP_SSL", "false") == "true"
		if skipSSL {
			log.Warn("STRATT_CHEF_SKIP_SSL enabled: Chef TLS verification is OFF (self-signed legacy servers only; estate data flows unverified)")
		}
		syncer := chef.NewSyncer(chef.Config{
			ServerURL:   serverURL,
			ClientName:  os.Getenv("STRATT_CHEF_CLIENT_NAME"),
			KeyPEM:      keyPEM,
			AuthVersion: env("STRATT_CHEF_AUTH_VERSION", "1.0"),
			SkipSSL:     skipSSL,
			SourceName:  env("STRATT_CHEF_SOURCE_NAME", "chef"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("chef syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no Chef Source configured (STRATT_CHEF_SERVER_URL empty); syncer idle")
	}

	// ── OpenVox/PuppetDB node Syncer (ADR-0038; config-mgmt SoR ingest) ──
	if pdbURL := os.Getenv("STRATT_PUPPETDB_URL"); pdbURL != "" {
		interval, err := time.ParseDuration(env("STRATT_PUPPETDB_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("puppet interval: %w", err)
		}
		// mTLS client cert/key/CA arrive as mounted files (§2.5: material stays
		// a file the process reads, never persisted); empty for an http:// dev URL.
		syncer := puppet.NewSyncer(puppet.Config{
			BaseURL:    pdbURL,
			CertFile:   os.Getenv("STRATT_PUPPETDB_CERT_FILE"),
			KeyFile:    os.Getenv("STRATT_PUPPETDB_KEY_FILE"),
			CAFile:     os.Getenv("STRATT_PUPPETDB_CA_FILE"),
			SourceName: env("STRATT_PUPPETDB_SOURCE_NAME", "puppet"),
		}, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("puppet syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no PuppetDB Source configured (STRATT_PUPPETDB_URL empty); syncer idle")
	}

	// ── Salt grains Syncer (ADR-0039; config-mgmt SoR ingest) ────────────
	saltCfg := salt.Config{
		APIURL:      os.Getenv("STRATT_SALT_API_URL"),
		Username:    os.Getenv("STRATT_SALT_USERNAME"),
		Password:    os.Getenv("STRATT_SALT_PASSWORD"),
		Eauth:       env("STRATT_SALT_EAUTH", "pam"),
		SourceName:  env("STRATT_SALT_SOURCE_NAME", "salt"),
		EmitterName: env("STRATT_SALT_EMITTER_NAME", "salt"),
		EventTags:   splitNonEmpty(os.Getenv("STRATT_SALT_EVENT_TAGS")),
	}
	if saltCfg.APIURL != "" {
		interval, err := time.ParseDuration(env("STRATT_SALT_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("salt interval: %w", err)
		}
		syncer := salt.NewSyncer(saltCfg, interval, store, log)
		if err := syncer.Register(ctx); err != nil {
			return err
		}
		go func() {
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("salt syncer stopped", "error", err)
			}
		}()
	} else {
		log.Info("no Salt Source configured (STRATT_SALT_API_URL empty); syncer idle")
	}

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
		go func() {
			if err := ctl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("desired-state controller stopped", "error", err)
			}
		}()

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
			if fga != nil {
				// OpenFGA is a projection of the same Git source (§1.2):
				// desired-state sync, adds and revokes both.
				if err := fga.SyncTuples(ctx, evaluator.Snapshot()); err != nil {
					log.Error("openfga tuple sync failed; server grants may be stale", "error", err)
				}
			}
		}
		reloadTuples()
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reloadTuples()
				}
			}
		}()

		// Declared Triggers project onto Temporal Schedules on the same
		// cadence (§3: Temporal owns all lifecycle; ADR-0010) — Git declares,
		// the graph row is the first projection, the Schedule the second.
		trigReconciler := &triggers.Reconciler{
			Temporal: temporalClient, Store: store, Log: log, Interval: interval,
		}
		go func() {
			if err := trigReconciler.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("trigger reconciler stopped", "error", err)
			}
		}()

		// Declared Baseline cadences project onto Temporal Schedules the same
		// way (§3: "Baseline cadences"; ADR-0019).
		blReconciler := &baselines.Reconciler{
			Temporal: temporalClient, Store: store, Log: log, Interval: interval,
		}
		go func() {
			if err := blReconciler.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("baseline reconciler stopped", "error", err)
			}
		}()
	} else {
		log.Info("no desired-state checkout configured (STRATT_DESIRED_STATE_PATH empty); reconciliation off — authz has no tuples (deny-all), triggers idle")
	}

	// ── trigger engine (ADR-0018: Emitter events × CEL → launches) ───────
	engine := &triggerengine.Engine{Store: store, Bus: bus, Temporal: temporalClient, Log: log}
	go func() {
		if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("trigger engine stopped", "error", err)
		}
	}()

	// ── Salt event-bus Emitter (ADR-0039: stream-subscriber → emitter stream) ─
	if saltCfg.APIURL != "" && env("STRATT_SALT_EVENTS", "false") == "true" {
		if len(saltCfg.EventTags) == 0 {
			log.Warn("STRATT_SALT_EVENTS enabled with no STRATT_SALT_EVENT_TAGS filter: forwarding the ENTIRE Salt event bus onto the emitter stream (set a tag-prefix allowlist to avoid flooding)")
		}
		emitter := salt.NewEmitter(saltCfg, bus, log)
		go func() {
			if err := emitter.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("salt emitter stopped", "error", err)
			}
		}()
	}

	// ── notifier (ADR-0027: Notices × Subscriptions → webhook delivery) ──
	// The outbound mirror of the trigger engine. Each delivery runs in a pod
	// so the Sink's CredentialRef is injected at spawn (§2.5) — the daemon
	// composes pod specs from pointers, never material.
	notifier := &notify.Dispatcher{Store: store, Bus: bus, Dispatcher: dispatcher, Authz: authorizer, Log: log}
	go func() {
		if err := notifier.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("notifier stopped", "error", err)
		}
	}()

	// Audit sealer (ADR-0034): the single writer that chains the append-only
	// audit ledger for tamper-evidence, decoupled from the hot-path append so
	// integrity never bottlenecks the full access log (§1.6, §1.8).
	go (&audit.Sealer{Store: store, Log: log}).Run(ctx)

	// ── interface plane ──────────────────────────────────────────────────
	uiDir := os.Getenv("STRATT_UI_DIR")
	if uiDir != "" {
		log.Info("serving ui", "dir", uiDir)
	}
	server := &api.Server{Store: store, Bus: bus, Temporal: temporalClient, Authz: authorizer, Log: log, DevPrincipalHeader: devPrincipal, OIDC: oidcResolver, UIDir: uiDir, StateBackend: stateHandler, EmitterIngest: emitters.New(store, bus, log).Handler(), SCIM: scim.New(store, log).Handler(), CompileStatus: compileStatus, Evidence: evidence, SiteLiveness: func(ctx context.Context) (map[string]bool, error) {
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
