package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"atryum/internal/api"
	"atryum/internal/auth"
	backendclient "atryum/internal/backend"
	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) <= 1 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Println(globalUsage())
		return
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runServer(os.Args[2:])
	case "setup":
		err = runSetup(os.Args[2:])
	case "hooks":
		err = runHooks(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], globalUsage())
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runServer(args []string) error {
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

	invRepo := store.NewInvocationRepoWithDialect(db, dialect)
	eventRepo := store.NewEventRepoWithDialect(db, dialect)
	serverRepo := store.NewServerRepoWithDialect(db, dialect)
	oauthRepo := store.NewOAuthRepoWithDialect(db, dialect)
	rulesRepo := store.NewRulesRepoWithDialect(db, dialect)
	agentsRepo := store.NewAgentsRepoWithDialect(db, dialect)
	agentSyncSettingsRepo := store.NewAgentSyncSettingsRepoWithDialect(db, dialect)

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
		for _, a := range agentsResp.Results {
			description, _ := a.CustomFields["description"].(string)
			log.Printf("  agent cuid=%s name=%q description=%q", a.CUID, a.Name, description)
			if upsertErr := agentsRepo.Upsert(ctx, store.AgentRecord{
				ID:                 uuid.NewString(),
				VMOrganizationCUID: agentsResp.OrgCUID,
				VMOrganizationName: agentsResp.OrgName,
				VMCUID:             a.CUID,
				VMName:             a.Name,
				VMDescription:      description,
				Enabled:            true,
				SyncedAt:           syncedAt,
			}); upsertErr != nil {
				log.Printf("  agent sync upsert failed for cuid=%s: %v", a.CUID, upsertErr)
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
	resolver := mcp.NewResolver(serverRepo, cfg).WithCredentials(credentialAdapter{repo: oauthRepo})
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		return fmt.Errorf("bootstrap servers: %w", err)
	}
	client := mcp.NewHTTPClient()

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
		invAgents = &agentsLookupAdapter{repo: agentsRepo}
	}
	var invEvaluator invocation.EvaluatorClient
	if backendClient != nil {
		invEvaluator = &evaluatorAdapter{client: backendClient}
	}

	service := invocation.NewService(invRepo, eventRepo, resolver, client, policyRegistry, time.Duration(cfg.Defaults.RequestTimeoutSeconds)*time.Second, rulesRepo, invAgents, invEvaluator, &syncSettingsAdapter{repo: agentSyncSettingsRepo})
	if backendClient != nil {
		service.SetInvocationSummarizer(&summaryAdapter{client: backendClient})
	}
	serverAdmin := api.NewServerAdminService(serverRepo, oauthRepo, client, 5*time.Second, cfg.Server.PublicBaseURL)
	if *initServers {
		if err := initEnabledServerStatuses(context.Background(), serverRepo, serverAdmin); err != nil {
			return fmt.Errorf("init servers: %w", err)
		}
	}
	handler := api.NewHandler(service, serverAdmin, policyRegistry, rulesRepo, agentsRepo, agentSyncSettingsRepo, syncAgents, backendClient)

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
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
	repo *store.AgentsRepo
}

func (a *agentsLookupAdapter) GetByAgentID(ctx context.Context, agentID string) (invocation.AgentRecord, error) {
	rec, err := a.repo.GetByAgentID(ctx, agentID)
	if err != nil {
		return invocation.AgentRecord{}, err
	}
	return invocation.AgentRecord{ID: rec.ID, VMCUID: rec.VMCUID, VMOrganizationCUID: rec.VMOrganizationCUID}, nil
}

func (a *agentsLookupAdapter) GetByVMCUID(ctx context.Context, vmCUID string) (invocation.AgentRecord, error) {
	rec, err := a.repo.GetByVMCUID(ctx, vmCUID)
	if err != nil {
		return invocation.AgentRecord{}, err
	}
	return invocation.AgentRecord{ID: rec.ID, VMCUID: rec.VMCUID, VMOrganizationCUID: rec.VMOrganizationCUID}, nil
}

// syncSettingsAdapter bridges store.AgentSyncSettingsRepo → invocation.SyncSettingsProvider.
// ConstitutionFieldKey is read from the DB on every call so that changes saved
// via the Settings UI take effect immediately without a restart.
type syncSettingsAdapter struct {
	repo *store.AgentSyncSettingsRepo
}

func (a *syncSettingsAdapter) ConstitutionFieldKey(ctx context.Context) string {
	s, _ := a.repo.Get(ctx)
	return s.ConstitutionFieldKey
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
		ModelConfigCUID:      req.ModelConfigCUID,
		OrgCUID:              req.OrgCUID,
		AgentVMCUID:          req.AgentVMCUID,
		ConstitutionFieldKey: req.ConstitutionFieldKey,
		ServerName:           req.ServerName,
		ToolName:             req.ToolName,
		ToolArgs:             req.ToolArgs,
		Context:              req.Context,
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

// credentialAdapter bridges store.OAuthRepo into the narrow
// mcp.CredentialStore interface the resolver consumes. Keeps the mcp
// package independent of the concrete OAuthRepo/OAuthCredential types.
type credentialAdapter struct {
	repo *store.OAuthRepo
}

func (a credentialAdapter) GetCredential(ctx context.Context, serverName string) (mcp.AccessTokenView, error) {
	cred, err := a.repo.GetCredential(ctx, serverName)
	if err != nil {
		return mcp.AccessTokenView{}, err
	}
	return mcp.AccessTokenView{AccessToken: cred.AccessToken}, nil
}
