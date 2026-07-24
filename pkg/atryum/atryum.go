// Package atryum exposes the atryum CLI and server as an importable library
// so downstream programs can build extended binaries: a thin main calls Main
// with Options (extra routes, embedded notices) instead of patching sources.
// The stock binary in cmd/atryum is exactly such a program.
package atryum

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/validmind/atryum/internal/api"
	"github.com/validmind/atryum/internal/auth"
	backendclient "github.com/validmind/atryum/internal/backend"
	"github.com/validmind/atryum/internal/config"
	"github.com/validmind/atryum/internal/invocation"
	"github.com/validmind/atryum/internal/invocation/policy"
	"github.com/validmind/atryum/internal/managedagents"
	"github.com/validmind/atryum/internal/mcp"
	"github.com/validmind/atryum/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Main dispatches os.Args to the run/setup/hooks/licenses commands and
// terminates the process on error. It is the entire body of the stock
// atryum binary; embedding programs call it from their own main.
func Main(opts ...Option) {
	o := buildOptions(opts)

	if len(os.Args) <= 1 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Println(globalUsage())
		return
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runServer(os.Args[2:], o)
	case "setup":
		err = runSetup(os.Args[2:])
	case "hooks":
		err = runHooks(os.Args[2:])
	case "licenses":
		err = runLicenses(o)
	case "version", "--version", "-v":
		fmt.Println(versionString())
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], globalUsage())
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runServer(args []string, o options) error {
	if hasHelpArg(args) {
		fmt.Println(runUsage())
		return nil
	}

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "path to TOML config")
	initServers := fs.Bool("init-servers", false, "test all enabled MCP servers on startup")
	if err := fs.Parse(args); err != nil {
		return errors.New(runUsage())
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %v\n%s", fs.Args(), runUsage())
	}

	resolvedConfigPath, err := resolveStartupConfigPath(*configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := configureLogging(cfg.Server.LogLevel); err != nil {
		return err
	}
	backendClient, err := backendclient.NewClient(cfg.Backend)
	if err != nil {
		return fmt.Errorf("backend config: %w", err)
	}
	if backendClient != nil {
		resp, err := backendClient.CheckConnection(context.Background())
		if err != nil {
			return fmt.Errorf("backend connection check: %w", err)
		}
		if backendClient.AuthMode() == "api_key" {
			log.Printf("backend connection verified with API credentials for org=%s (%s)", resp.OrgCUID, resp.OrgName)
		} else {
			log.Printf("backend connection verified for service=%s machine_user_cuid=%s", resp.ServiceName, resp.MachineUserCUID)
		}
	} else {
		log.Printf("backend connection check skipped (backend.base_url not configured)")
	}

	db, dialect, err := store.OpenDatabase(cfg.Server.DatabaseURL, cfg.Server.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := store.InitDBWithDialect(db, dialect); err != nil {
		return fmt.Errorf("init db: %w", err)
	}

	for _, em := range o.extensionMigrations {
		if err := store.RunExtensionMigrationsWithDialect(db, dialect, em.namespace, em.definitions); err != nil {
			return fmt.Errorf("apply extension migrations: %w", err)
		}
	}
	for _, hook := range o.databaseHooks {
		hook(db, dialect == store.DialectPostgres)
	}

	invRepo := store.NewInvocationRepoWithDialect(db, dialect)
	eventRepo := store.NewEventRepoWithDialect(db, dialect)
	serverRepo := store.NewServerRepoWithDialect(db, dialect)
	oauthRepo := store.NewOAuthRepoWithDialect(db, dialect)
	rulesRepo := store.NewRulesRepoWithDialect(db, dialect)
	agentsRepo := store.NewAgentsRepoWithDialect(db, dialect)
	managedAgentBindingRepo := store.NewManagedAgentBindingRepoWithDialect(db, dialect)
	agentSyncSettingsRepo := store.NewAgentSyncSettingsRepoWithDialect(db, dialect)
	llmConfigsRepo := store.NewLLMConfigsRepoWithDialect(db, dialect)
	plansRepo := store.NewPlansRepoWithDialect(db, dialect)
	planEventsRepo := store.NewPlanEventsRepoWithDialect(db, dialect)

	// syncAgents is the shared sync function used both at startup and via the
	// admin API POST /api/v1/admin/agents/sync endpoint.
	// org_cuid and agent_record_type_slug are read exclusively from the DB
	// (configured via the Settings UI).
	syncAgents := func(ctx context.Context) error {
		if backendClient == nil {
			return fmt.Errorf("backend not configured")
		}
		settings, _ := agentSyncSettingsRepo.Get(ctx)
		if settings.OrgCUID == "" || settings.AgentRecordTypeSlug == "" {
			return fmt.Errorf("agent sync requires org_cuid and agent_record_type_slug — configure them in the Settings UI")
		}
		agentsResp, err := backendClient.FetchAgents(ctx, settings.OrgCUID, settings.AgentRecordTypeSlug)
		if err != nil {
			return fmt.Errorf("fetch agents: %w", err)
		}
		log.Printf("agent sync: fetched %d agent(s) for org=%s (%s) record_type=%s", agentsResp.Total, agentsResp.OrgCUID, agentsResp.OrgName, settings.AgentRecordTypeSlug)
		syncedAt := time.Now().UTC()
		charterKey := settings.CharterFieldKey
		activeCUIDs := make([]string, 0, len(agentsResp.Results))
		for _, a := range agentsResp.Results {
			description, _ := a.CustomFields["description"].(string)
			// Pull the charter from the configured custom field. Tolerate both a
			// flat custom_fields map and one nested under "model".
			charter := ""
			if charterKey != "" {
				if v, ok := a.CustomFields[charterKey].(string); ok {
					charter = v
				} else if model, ok := a.CustomFields["model"].(map[string]any); ok {
					charter, _ = model[charterKey].(string)
				}
			}
			log.Printf("  agent cuid=%s name=%q description=%q charter_key=%q charter_len=%d", a.CUID, a.Name, description, charterKey, len(charter))
			if upsertErr := agentsRepo.Upsert(ctx, store.AgentRecord{
				ID:                 uuid.NewString(),
				VMOrganizationCUID: agentsResp.OrgCUID,
				VMOrganizationName: agentsResp.OrgName,
				VMCUID:             a.CUID,
				VMName:             a.Name,
				VMDescription:      description,
				Charter:            charter,
				Enabled:            true,
				SyncedAt:           syncedAt,
			}); upsertErr != nil {
				log.Printf("  agent sync upsert failed for cuid=%s: %v", a.CUID, upsertErr)
			} else {
				activeCUIDs = append(activeCUIDs, a.CUID)
			}
		}
		// Prune agents that are no longer present in ValidMind (archived or deleted).
		// Agents with managed agent bindings are never pruned: deleting them would
		// cascade-delete their binding configuration (ON DELETE CASCADE FK).
		boundCUIDs, err := agentsRepo.ListVMCUIDsWithBindings(ctx)
		if err != nil {
			log.Printf("agent sync: could not list agents with bindings, skipping stale prune: %v", err)
		} else {
			keepCUIDs := append(activeCUIDs, boundCUIDs...)
			if err := agentsRepo.DeleteSyncedStaleForOrg(ctx, agentsResp.OrgCUID, keepCUIDs); err != nil {
				log.Printf("agent sync: failed to prune stale agents for org=%s: %v", agentsResp.OrgCUID, err)
			}
		}
		return nil
	}

	// Attempt startup sync when DB settings are already configured.
	{
		startupSettings, _ := agentSyncSettingsRepo.Get(context.Background())
		if backendClient != nil && startupSettings.OrgCUID != "" && startupSettings.AgentRecordTypeSlug != "" {
			if err := syncAgents(context.Background()); err != nil {
				log.Printf("agent sync failed: %v", err)
			}
		}
	}
	client := mcp.NewHTTPClient()
	resolver := mcp.NewResolver(serverRepo, cfg).WithCredentials(store.NewRefreshingOAuthCredentialStore(oauthRepo, client))
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		return fmt.Errorf("bootstrap servers: %w", err)
	}

	manualApproval := policy.ManualApprovalProvider{}
	policyRegistry := policy.NewRegistry(
		policy.AlwaysApproveProvider{},
		manualApproval,
		policy.AlwaysDenyProvider{},
	)
	providerID := cfg.Policy.Provider
	if providerID == "" {
		providerID = "manual_approval"
	}
	if err := policyRegistry.SetActive(providerID); err != nil {
		return fmt.Errorf("policy provider: %w", err)
	}
	log.Printf("policy provider: %s", policyRegistry.Active().DisplayName())

	// Wrap store and backend client in thin adapters that satisfy the
	// invocation package interfaces without creating import cycles.
	var invAgents invocation.AgentLookup
	if agentsRepo != nil {
		invAgents = &agentsLookupAdapter{repo: agentsRepo, managedBindings: managedAgentBindingRepo}
	}

	// Build the evaluator: always create a local evaluator backed by llmConfigsRepo.
	// When a ValidMind backend is configured, wrap both in a dispatchingEvaluator.
	localEvaluator := invocation.NewLocalEvaluatorClient(&llmConfigsLookupAdapter{repo: llmConfigsRepo})
	var invEvaluator invocation.EvaluatorClient
	if backendClient != nil {
		invEvaluator = &invocation.DispatchingEvaluator{
			VM:    &evaluatorAdapter{client: backendClient},
			Local: localEvaluator,
		}
	} else {
		invEvaluator = localEvaluator
	}

	service := invocation.NewService(invRepo, eventRepo, resolver, client, policyRegistry, time.Duration(cfg.Defaults.RequestTimeoutSeconds)*time.Second, rulesRepo, invAgents, invEvaluator, &syncSettingsAdapter{repo: agentSyncSettingsRepo})
	service.SetPlanStore(plansRepo, planEventsRepo, localEvaluator, planStatusPollOrigins(cfg.Server))
	service.SetPlanTTLBounds(cfg.Plans.DefaultTTLSeconds, cfg.Plans.MaxTTLSeconds)
	if backendClient != nil {
		service.SetInvocationSummarizer(&summaryAdapter{client: backendClient})
	}
	service.SetSessionStore(store.NewExternalSessionRepoWithDialect(db, dialect))
	service.SetStreamOptions(
		mcp.StreamOptions{
			HeaderTimeout:   time.Duration(cfg.Defaults.EffectiveStreamHeaderTimeoutSeconds()) * time.Second,
			IdleTimeout:     time.Duration(cfg.Defaults.StreamIdleTimeoutSeconds) * time.Second,
			MaxDuration:     time.Duration(cfg.Defaults.StreamMaxDurationSeconds) * time.Second,
			MaxMessageBytes: cfg.Defaults.StreamMaxMessageBytes,
		},
		invocation.StreamAuditLimits{
			MaxEvents:     cfg.Defaults.StreamAuditMaxEvents,
			MaxEventBytes: cfg.Defaults.StreamAuditMaxEventBytes,
		},
	)
	serverAdmin := api.NewServerAdminService(serverRepo, oauthRepo, client, 5*time.Second, cfg.Server.PublicBaseURL)
	if *initServers {
		if err := initEnabledServerStatuses(context.Background(), serverRepo, serverAdmin); err != nil {
			return fmt.Errorf("init servers: %w", err)
		}
	}
	// Only wire up the sync function when a backend client is available; passing
	// nil causes the handler to skip the post-save sync trigger entirely.
	var syncAgentsFn func(ctx context.Context) error
	if backendClient != nil {
		syncAgentsFn = syncAgents
	}
	handler := api.NewHandler(service, serverAdmin, policyRegistry, rulesRepo, agentsRepo, agentSyncSettingsRepo, llmConfigsRepo, syncAgentsFn, backendClient, localEvaluator)
	handler.SetManagedAgentBindings(managedAgentBindingRepo)
	handler.SetStreamRelayEnabled(cfg.Defaults.StreamRelayEnabled)
	for _, register := range o.extraRoutes {
		handler.AddExtraRoutes(register)
	}

	authValidator, err := auth.NewValidator(cfg.Auth, nil)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if authValidator != nil {
		handler.SetAuthValidator(authValidator)
		log.Printf("inbound auth enabled (%d issuer(s))", len(authValidator.Configs()))
	} else {
		log.Printf("inbound auth disabled (no [[auth]] section configured)")
	}
	handler.SetAPIKeyAuth(cfg.APIKey)
	if cfg.APIKey.Enabled() {
		log.Printf("api key auth enabled for /invocations/{agent_id} and /agent_ids")
	} else {
		log.Printf("api key auth NOT configured: /invocations/{agent_id} and /agent_ids will refuse all requests")
	}
	authDebugSkipVerify := cfg.AuthDebug.SkipVerify || truthyEnv("ATRYUM_AUTH_DEBUG_SKIP_VERIFY")
	if authDebugSkipVerify {
		handler.SetAuthDebugSkipVerify(true)
		log.Printf("WARNING: inbound auth debug skip_verify enabled; /mcp/ Authorization header is ignored entirely")
	}

	// Optional Claude Managed Agents events bridge. Enabled only when an
	// Anthropic API key is configured (TOML or env). It watches registered
	// sessions, streams their events into the invocations table, and gates
	// blocking tool calls through the normal approval rules.
	var managedSvc *managedagents.Service
	var managedAccounts []managedagents.Account
	for _, ma := range cfg.ManagedAgents {
		if ma.APIKey == "" {
			continue
		}
		if ma.Workspace == "" {
			return fmt.Errorf("managed_agents workspace label is required for account %q; use an Anthropic API key created in that workspace", emptyDefault(ma.Name, managedagents.DefaultAccountName))
		}
		acctCfg := managedagents.Config{
			Name:                    ma.Name,
			Workspace:               ma.Workspace,
			BaseURL:                 ma.BaseURL,
			APIKey:                  ma.APIKey,
			PollInterval:            time.Duration(ma.PollIntervalMillis) * time.Millisecond,
			ReconnectBackoff:        time.Duration(ma.ReconnectBackoffSeconds) * time.Second,
			RecentChatMessagesLimit: ma.RecentChatMessagesLimit,
			ClientName:              ma.ClientName,
			ClientVersion:           ma.ClientVersion,
		}
		managedAccounts = append(managedAccounts, managedagents.Account{
			Client: managedagents.NewAnthropicHTTPClient(acctCfg),
			Config: acctCfg,
		})
	}
	if len(managedAccounts) > 0 {
		managedSessionRepo := store.NewManagedAgentSessionRepoWithDialect(db, dialect)
		managedSvc, err = managedagents.NewService(
			service, // *invocation.Service satisfies InvocationGateway
			&managedSessionStoreAdapter{repo: managedSessionRepo},
			&managedAuditAdapter{inv: invRepo, events: eventRepo},
			managedAccounts,
		)
		if err != nil {
			return fmt.Errorf("configure managed agents bridge: %w", err)
		}
		instanceName := cfg.Server.AtryumInstance
		if instanceName == "" {
			instanceName = cfg.Server.PublicBaseURL
		}
		managedSvc.SetInstanceName(instanceName)
		managedSvc.SetBindings(&managedBindingStoreAdapter{repo: managedAgentBindingRepo})
		if err := managedSvc.Start(context.Background()); err != nil {
			return fmt.Errorf("start managed agents bridge: %w", err)
		}
		handler.SetManagedAgents(managedSvc)
		log.Printf("claude managed agents bridge enabled (%d account(s))", len(managedAccounts))
	} else {
		log.Printf("claude managed agents bridge disabled (no [[managed_agents]] entry with api_key)")
	}

	srv := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("atryum listening on %s", cfg.Server.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	if managedSvc != nil {
		_ = managedSvc.Close()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}

// planStatusPollOrigins lists the hosts this Atryum API answers on, for the
// plan-status fast pass: the configured public base URL plus the loopback
// variants of the listen port. A status poll addressed to any other host is
// referred to the adherence judge instead of being fast-passed.
func planStatusPollOrigins(server config.ServerConfig) []string {
	var origins []string
	if server.PublicBaseURL != "" {
		origins = append(origins, server.PublicBaseURL)
	}
	if _, port, err := net.SplitHostPort(server.ListenAddr); err == nil && port != "" {
		origins = append(origins,
			"http://localhost:"+port, "http://127.0.0.1:"+port, "http://[::1]:"+port)
	}
	return origins
}

// managedSessionStoreAdapter bridges store.ManagedAgentSessionRepo →
// managedagents.SessionStore, converting between the store row and the
// managedagents registration type.
type managedSessionStoreAdapter struct {
	repo *store.ManagedAgentSessionRepo
}

func (a *managedSessionStoreAdapter) Upsert(ctx context.Context, s managedagents.SessionRegistration) error {
	return a.repo.Upsert(ctx, store.ManagedAgentSession{
		SessionID:   s.SessionID,
		Account:     s.Account,
		AgentID:     s.AgentID,
		Description: s.Description,
		LastEventID: s.LastEventID,
		CreatedAt:   s.CreatedAt,
	})
}

func (a *managedSessionStoreAdapter) Get(ctx context.Context, sessionID string) (managedagents.SessionRegistration, error) {
	row, err := a.repo.Get(ctx, sessionID)
	if err != nil {
		return managedagents.SessionRegistration{}, err
	}
	return managedSessionToReg(row), nil
}

func (a *managedSessionStoreAdapter) List(ctx context.Context) ([]managedagents.SessionRegistration, error) {
	rows, err := a.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]managedagents.SessionRegistration, 0, len(rows))
	for _, row := range rows {
		out = append(out, managedSessionToReg(row))
	}
	return out, nil
}

func (a *managedSessionStoreAdapter) Delete(ctx context.Context, sessionID string) error {
	return a.repo.Delete(ctx, sessionID)
}

func (a *managedSessionStoreAdapter) UpdateCursor(ctx context.Context, sessionID, lastEventID string) error {
	return a.repo.UpdateCursor(ctx, sessionID, lastEventID)
}

func managedSessionToReg(row store.ManagedAgentSession) managedagents.SessionRegistration {
	return managedagents.SessionRegistration{
		SessionID:   row.SessionID,
		Account:     row.Account,
		AgentID:     row.AgentID,
		Description: row.Description,
		LastEventID: row.LastEventID,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

type managedBindingStoreAdapter struct {
	repo *store.ManagedAgentBindingRepo
}

func (a *managedBindingStoreAdapter) List(ctx context.Context) ([]managedagents.AgentBinding, error) {
	rows, err := a.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]managedagents.AgentBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, managedagents.AgentBinding{
			AgentCUID:       row.AgentCUID,
			Account:         row.Account,
			ClaudeAgentID:   row.ClaudeAgentID,
			ClaudeAgentName: row.ClaudeAgentName,
		})
	}
	return out, nil
}

// managedAuditAdapter bridges the invocation/event repos →
// managedagents.InvocationAuditStore for the synthetic per-session audit row.
type managedAuditAdapter struct {
	inv    *store.InvocationRepo
	events *store.EventRepo
}

func (a *managedAuditAdapter) Create(ctx context.Context, inv invocation.Invocation) error {
	return a.inv.Create(ctx, inv)
}

func (a *managedAuditAdapter) GetByIdempotencyKey(ctx context.Context, key string) (invocation.Invocation, error) {
	return a.inv.GetByIdempotencyKey(ctx, key)
}

func (a *managedAuditAdapter) CreateEvent(ctx context.Context, evt invocation.Event) error {
	return a.events.Create(ctx, evt)
}

func (a *managedAuditAdapter) ListEvents(ctx context.Context, invocationID string, filter invocation.EventListFilter) ([]invocation.Event, int, error) {
	return a.events.ListByInvocation(ctx, invocationID, filter)
}

func initEnabledServerStatuses(ctx context.Context, repo *store.ServerRepo, serverAdmin *api.ServerAdminService) error {
	enabled := true
	const pageSize = 100
	var offset uint64
	for {
		servers, total, err := repo.ListServers(ctx, mcp.ServerFilter{Enabled: &enabled, Offset: offset, Limit: pageSize})
		if err != nil {
			return err
		}
		if len(servers) == 0 {
			break
		}
		for _, server := range servers {
			result, err := serverAdmin.Test(ctx, server.Name)
			if err != nil {
				log.Printf("startup server init failed for %s: %v", server.Name, err)
				continue
			}
			log.Printf("startup server init %s: ok=%t connection=%s auth=%s", server.Name, result.Ok, result.ConnectionStatus, result.AuthStatus)
		}
		offset += uint64(len(servers))
		if int(offset) >= total {
			break
		}
	}
	return nil
}

func resolveStartupConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	if _, err := os.Stat("./atryum.toml"); err == nil {
		return "./atryum.toml", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	userCfgPath, err := defaultUserConfigPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(userCfgPath); err == nil {
		return userCfgPath, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	return "./atryum.toml", nil
}

func defaultUserConfigPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "atryum", "atryum.toml"), nil
}

func defaultUserDatabasePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "atryum", "atryum.db"), nil
}

// agentsLookupAdapter bridges store.AgentsRepo → invocation.AgentLookup.
type agentsLookupAdapter struct {
	repo            *store.AgentsRepo
	managedBindings *store.ManagedAgentBindingRepo
}

// parseAgentIDs decodes the agents.agent_ids JSON array column. Malformed or
// empty input yields nil rather than an error: the column is Atryum-managed
// (never user-supplied free text at this layer), and a lookup miss here
// should widen matching to nothing extra, not fail the caller.
func parseAgentIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

func (a *agentsLookupAdapter) GetByAgentID(ctx context.Context, agentID string) (invocation.AgentRecord, error) {
	rec, err := a.repo.GetByAgentID(ctx, agentID)
	if err == nil {
		return invocation.AgentRecord{ID: rec.ID, VMCUID: rec.VMCUID, VMOrganizationCUID: rec.VMOrganizationCUID, Charter: rec.Charter, AgentIDs: parseAgentIDs(rec.AgentIDs)}, nil
	}
	if a.managedBindings == nil {
		return invocation.AgentRecord{}, err
	}
	binding, bindErr := a.managedBindings.GetByClaudeAgentID(ctx, "", agentID)
	if bindErr != nil {
		return invocation.AgentRecord{}, err
	}
	rec, err = a.repo.Get(ctx, binding.AgentCUID)
	if err != nil {
		return invocation.AgentRecord{}, err
	}
	return invocation.AgentRecord{ID: rec.ID, VMCUID: rec.VMCUID, VMOrganizationCUID: rec.VMOrganizationCUID, Charter: rec.Charter, AgentIDs: parseAgentIDs(rec.AgentIDs)}, nil
}

func (a *agentsLookupAdapter) GetByVMCUID(ctx context.Context, vmCUID string) (invocation.AgentRecord, error) {
	rec, err := a.repo.GetByVMCUID(ctx, vmCUID)
	if err != nil {
		return invocation.AgentRecord{}, err
	}
	return invocation.AgentRecord{ID: rec.ID, VMCUID: rec.VMCUID, VMOrganizationCUID: rec.VMOrganizationCUID, Charter: rec.Charter, AgentIDs: parseAgentIDs(rec.AgentIDs)}, nil
}

// llmConfigsLookupAdapter bridges store.LLMConfigsRepo → invocation.LLMConfigProvider.
type llmConfigsLookupAdapter struct {
	repo *store.LLMConfigsRepo
}

func (a *llmConfigsLookupAdapter) GetLLMConfig(ctx context.Context, id string) (invocation.LocalLLMConfig, error) {
	cfg, err := a.repo.Get(ctx, id)
	if err != nil {
		return invocation.LocalLLMConfig{}, err
	}
	return toLocalLLMConfig(cfg), nil
}

// DefaultLLMConfig returns the first enabled LLM config, for evaluator calls
// that carry no rule-specific config ID (e.g. the plan adherence judge
// reviewing a human-approved plan).
func (a *llmConfigsLookupAdapter) DefaultLLMConfig(ctx context.Context) (invocation.LocalLLMConfig, error) {
	configs, err := a.repo.List(ctx)
	if err != nil {
		return invocation.LocalLLMConfig{}, err
	}
	for _, cfg := range configs {
		if cfg.Enabled {
			return toLocalLLMConfig(cfg), nil
		}
	}
	return invocation.LocalLLMConfig{}, fmt.Errorf("no enabled LLM config available")
}

func toLocalLLMConfig(cfg store.LLMConfig) invocation.LocalLLMConfig {
	return invocation.LocalLLMConfig{
		ID:       cfg.ID,
		Provider: string(cfg.Provider),
		Model:    cfg.Model,
		APIKey:   cfg.APIKey,
		BaseURL:  cfg.BaseURL,
	}
}

// syncSettingsAdapter bridges store.AgentSyncSettingsRepo → invocation.SyncSettingsProvider.
// CharterFieldKey is read from the DB on every call so that changes saved
// via the Settings UI take effect immediately without a restart.
type syncSettingsAdapter struct {
	repo *store.AgentSyncSettingsRepo
}

func (a *syncSettingsAdapter) CharterFieldKey(ctx context.Context) string {
	s, _ := a.repo.Get(ctx)
	return s.CharterFieldKey
}

func (a *syncSettingsAdapter) DefaultAgentVMCUID(ctx context.Context) string {
	s, _ := a.repo.Get(ctx)
	return s.DefaultAgentVMCUID
}

func (a *syncSettingsAdapter) SummarySettings(ctx context.Context) (string, string) {
	s, _ := a.repo.Get(ctx)
	return s.OrgCUID, s.SummaryModelConfigCUID
}

// evaluatorAdapter bridges backendclient.Client → invocation.EvaluatorClient.
type evaluatorAdapter struct {
	client *backendclient.Client
}

func (e *evaluatorAdapter) EvaluateToolCall(ctx context.Context, req invocation.EvaluateRequest) (invocation.EvaluateResponse, error) {
	resp, err := e.client.EvaluateToolCall(ctx, backendclient.EvaluateRequest{
		ModelConfigCUID: req.ModelConfigCUID,
		OrgCUID:         req.OrgCUID,
		AgentVMCUID:     req.AgentVMCUID,
		CharterFieldKey: req.CharterFieldKey,
		ServerName:      req.ServerName,
		ToolName:        req.ToolName,
		ToolArgs:        req.ToolArgs,
		Context:         req.Context,
	})
	if err != nil {
		return invocation.EvaluateResponse{}, err
	}
	return invocation.EvaluateResponse{
		Verdict:    resp.Verdict,
		Reason:     resp.Reason,
		Confidence: resp.Confidence,
	}, nil
}

// summaryAdapter bridges backendclient.Client → invocation.SummaryClient.
type summaryAdapter struct {
	client *backendclient.Client
}

func (s *summaryAdapter) SummarizeInvocation(ctx context.Context, req invocation.SummaryRequest) (invocation.SummaryResponse, error) {
	resp, err := s.client.SummarizeInvocation(ctx, backendclient.SummarizeInvocationRequest{
		ModelConfigCUID: req.ModelConfigCUID,
		OrgCUID:         req.OrgCUID,
		Invocation:      req.Invocation,
	})
	if err != nil {
		return invocation.SummaryResponse{}, err
	}
	return invocation.SummaryResponse{Summary: resp.Summary}, nil
}

func truthyEnv(name string) bool {
	value := os.Getenv(name)
	return value == "1" || value == "true" || value == "TRUE" || value == "yes" || value == "YES"
}

func configureLogging(logLevel string) error {
	level, err := parseLogLevel(logLevel)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	return nil
}

func parseLogLevel(logLevel string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(logLevel)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid server.log_level %q", logLevel)
	}
}

func emptyDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
