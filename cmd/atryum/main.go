package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	configPath := flag.String("config", "./atryum.toml", "path to TOML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	backendClient, err := backendclient.NewClient(cfg.Backend)
	if err != nil {
		log.Fatalf("backend config: %v", err)
	}
	if backendClient != nil {
		resp, err := backendClient.CheckConnection(context.Background())
		if err != nil {
			log.Fatalf("backend connection check: %v", err)
		}
		log.Printf("backend connection verified for service=%s machine_user_cuid=%s", resp.ServiceName, resp.MachineUserCUID)
	} else {
		log.Printf("backend connection check skipped (backend.base_url not configured)")
	}

	db, dialect, err := store.OpenDatabase(cfg.Server.DatabaseURL, cfg.Server.DatabasePath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := store.InitDBWithDialect(db, dialect); err != nil {
		log.Fatalf("init db: %v", err)
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
		log.Fatalf("bootstrap servers: %v", err)
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
		log.Fatalf("policy provider: %v", err)
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

	serviceSettings, _ := agentSyncSettingsRepo.Get(context.Background())
	service := invocation.NewService(invRepo, eventRepo, resolver, client, policyRegistry, time.Duration(cfg.Defaults.RequestTimeoutSeconds)*time.Second, rulesRepo, invAgents, invEvaluator, serviceSettings.ConstitutionFieldKey)
	serverAdmin := api.NewServerAdminService(serverRepo, oauthRepo, client, 5*time.Second, cfg.Server.PublicBaseURL)
	handler := api.NewHandler(service, serverAdmin, policyRegistry, rulesRepo, agentsRepo, agentSyncSettingsRepo, syncAgents, backendClient)

	authValidator, err := auth.NewValidator(cfg.Auth, nil)
	if err != nil {
		log.Fatalf("auth: %v", err)
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
	return invocation.EvaluateResponse{Approved: resp.Approved, Reason: resp.Reason}, nil
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
