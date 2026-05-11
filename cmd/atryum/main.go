package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"atryum/internal/api"
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
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		log.Fatalf("bootstrap servers: %v", err)
	}
	client := mcp.NewHTTPClient()

	timedApprove := policy.NewTimedApproveProvider(time.Duration(cfg.Policy.DurationMinutes) * time.Minute)
	policyRegistry := policy.NewRegistry(
		policy.AlwaysApproveProvider{},
		policy.AlwaysDenyProvider{},
		timedApprove,
	)
	providerID := cfg.Policy.Provider
	if providerID == "" {
		providerID = "always_approve"
	}
	if err := policyRegistry.SetActive(providerID); err != nil {
		log.Fatalf("policy provider: %v", err)
	}
	log.Printf("policy provider: %s", policyRegistry.Active().DisplayName())

	service := invocation.NewService(invRepo, eventRepo, resolver, client, policyRegistry, time.Duration(cfg.Defaults.RequestTimeoutSeconds)*time.Second)
	serverAdmin := api.NewServerAdminService(serverRepo, oauthRepo, client, 5*time.Second)
	handler := api.NewHandler(service, serverAdmin, policyRegistry)

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
