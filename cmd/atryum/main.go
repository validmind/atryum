package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sq "github.com/Masterminds/squirrel"

	"atryum/internal/api"
	"atryum/internal/config"
	"atryum/internal/invocation"
	"atryum/internal/mcp"
	"atryum/internal/store"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

func main() {
	configPath := flag.String("config", "./atryum.toml", "path to TOML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	db, err := openDB(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := initDB(cfg, db); err != nil {
		log.Fatalf("init db: %v", err)
	}

	invRepo := store.NewInvocationRepo(db)
	eventRepo := store.NewEventRepo(db)
	serverRepo := store.NewServerRepo(db)
	oauthRepo := store.NewOAuthRepo(db)
	resolver := mcp.NewResolver(serverRepo, cfg)
	if err := resolver.BootstrapIfEmpty(context.Background()); err != nil {
		log.Fatalf("bootstrap servers: %v", err)
	}
	client := mcp.NewHTTPClient()
	service := invocation.NewService(invRepo, eventRepo, resolver, client, time.Duration(cfg.Defaults.RequestTimeoutSeconds)*time.Second)
	serverAdmin := api.NewServerAdminService(serverRepo, oauthRepo, client, 5*time.Second)
	handler := api.NewHandler(service, serverAdmin)

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

func openDB(cfg config.Config) (*sql.DB, error) {
	driver := cfg.Server.DatabaseDriver
	if driver == "" || driver == "sqlite" {
		return sql.Open("sqlite", cfg.Server.DatabasePath)
	}
	store.Dialect = sq.Dollar
	return sql.Open("postgres", cfg.Server.DatabaseDSN)
}

func initDB(cfg config.Config, db *sql.DB) error {
	driver := cfg.Server.DatabaseDriver
	if driver == "" || driver == "sqlite" {
		return store.InitDB(db)
	}
	return store.InitPostgresDB(db)
}
