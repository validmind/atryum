package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"atryum/internal/api"
	"atryum/internal/claudeagents"
	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/invocation/policy"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// claudeAgentsSessionAdapter bridges the store's WatchableSession shape into
// the watcher's SessionRecord shape without coupling the two packages.
type claudeAgentsSessionAdapter struct {
	repo *store.ClaudeAgentsRepo
}

func (a claudeAgentsSessionAdapter) ListWatchableSessions(ctx context.Context) ([]claudeagents.SessionRecord, error) {
	rows, err := a.repo.ListWatchableSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]claudeagents.SessionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, claudeagents.SessionRecord{
			ID:              r.ID,
			AgentID:         r.AgentID,
			AgentName:       r.AgentName,
			SessionID:       r.SessionID,
			LastEventCursor: r.LastEventCursor,
		})
	}
	return out, nil
}

func (a claudeAgentsSessionAdapter) UpdateSessionCursor(ctx context.Context, id, cursor string) error {
	return a.repo.UpdateSessionCursor(ctx, id, cursor)
}

// EnsureAgent inserts a claude_agents row keyed on the Anthropic agent_id when
// none exists. Existing rows are left untouched so a human can edit name /
// description / enabled without discovery clobbering changes.
func (a claudeAgentsSessionAdapter) EnsureAgent(ctx context.Context, agentID, name, description string) error {
	if _, err := a.repo.GetAgentByAgentID(ctx, agentID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return a.repo.CreateAgent(ctx, store.ClaudeAgent{
		ID:          "cagent_" + uuid.NewString(),
		Name:        uniqueName(ctx, a.repo, name, agentID),
		AgentID:     agentID,
		Description: description,
		Enabled:     true,
	})
}

// EnsureSession inserts a claude_agent_sessions row when none exists for the
// given Anthropic session id. The Anthropic agent_id is resolved to the
// atryum-side row id first.
func (a claudeAgentsSessionAdapter) EnsureSession(ctx context.Context, agentID, sessionID string) error {
	if _, err := a.repo.GetSessionBySessionID(ctx, sessionID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	agent, err := a.repo.GetAgentByAgentID(ctx, agentID)
	if err != nil {
		return err
	}
	return a.repo.CreateSession(ctx, store.ClaudeAgentSession{
		ID:        "cagsess_" + uuid.NewString(),
		AgentID:   agent.ID,
		SessionID: sessionID,
	})
}

// uniqueName resolves a name collision by suffixing with a slice of the
// Anthropic agent id. The claude_agents.name column is UNIQUE; two Anthropic
// agents legitimately can share a display name.
func uniqueName(ctx context.Context, repo *store.ClaudeAgentsRepo, base, agentID string) string {
	if base == "" {
		base = agentID
	}
	if _, err := repo.GetAgentByName(ctx, base); errors.Is(err, sql.ErrNoRows) {
		return base
	}
	suffix := agentID
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return base + " (" + suffix + ")"
}

func main() {
	configPath := flag.String("config", "./atryum.toml", "path to TOML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
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
	claudeAgentsRepo := store.NewClaudeAgentsRepoWithDialect(db, dialect)
	resolver := mcp.NewResolver(serverRepo, cfg)
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

	service := invocation.NewService(invRepo, eventRepo, resolver, client, policyRegistry, time.Duration(cfg.Defaults.RequestTimeoutSeconds)*time.Second, rulesRepo)
	serverAdmin := api.NewServerAdminService(serverRepo, oauthRepo, client, 5*time.Second)
	handler := api.NewHandler(service, serverAdmin, policyRegistry, rulesRepo, claudeAgentsRepo)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// If a Claude Agents API key is configured, register the resolution
	// dispatcher (atryum -> Anthropic) and start the polling watcher
	// (Anthropic -> atryum). With no key the rest of atryum keeps working
	// exactly as before.
	if key := cfg.ClaudeAgents.APIKey; key != "" {
		caClient, err := claudeagents.New(claudeagents.Options{
			APIKey:  key,
			BaseURL: cfg.ClaudeAgents.BaseURL,
		})
		if err != nil {
			log.Fatalf("claude agents client: %v", err)
		}
		service.AddResolutionListener(claudeagents.NewDispatcher(caClient))

		toolUseHandler := func(ctx context.Context, evt claudeagents.ToolUseEvent) error {
			input := map[string]any{}
			if len(evt.Input) > 0 {
				// Best-effort: even if input isn't a JSON object, atryum
				// only needs to forward it. Failures are non-fatal.
				_ = json.Unmarshal(evt.Input, &input)
			}
			idemp := evt.ToolUseEventID
			_, err := service.Submit(ctx, invocation.ExternalSubmitRequest{
				Source:               evt.AgentName,
				Tool:                 evt.ToolName,
				Input:                input,
				IdempotencyKey:       &idemp,
				ThreadID:             evt.SessionID,
				ClaudeSessionID:      evt.SessionID,
				ClaudeToolUseEventID: evt.ToolUseEventID,
			})
			return err
		}

		watcher, err := claudeagents.NewWatcher(claudeagents.WatcherOptions{
			Client:       caClient,
			Store:        claudeAgentsSessionAdapter{repo: claudeAgentsRepo},
			Handler:      toolUseHandler,
			PollInterval: time.Duration(cfg.ClaudeAgents.PollIntervalSeconds) * time.Second,
		})
		if err != nil {
			log.Fatalf("claude agents watcher: %v", err)
		}
		go watcher.Start(ctx)
		log.Printf("claude agents watcher started")
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

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
