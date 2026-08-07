package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/identity"
	"omnora/internal/server"
	"omnora/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadEnv()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	if err := cfg.ValidateBusinessExposure(); err != nil {
		slog.Error("invalid HTTP trust configuration", "error", err)
		os.Exit(2)
	}
	if cfg.Routes.Enabled(domain.RouteGroupREST) || cfg.Routes.Enabled(domain.RouteGroupMCP) || cfg.Routes.Enabled(domain.RouteGroupShare) || cfg.Routes.Enabled(domain.RouteGroupMemberWeb) || cfg.Routes.Enabled(domain.RouteGroupAdminWeb) || cfg.Routes.Enabled(domain.RouteGroupOpenAPI) {
		if err := cfg.ValidateAuditHMACKey(); err != nil {
			slog.Error("invalid audit HMAC configuration", "error", err)
			os.Exit(2)
		}
	}
	configureLogger(cfg.Log, os.Stdout)

	var db *store.DB
	if cfg.Database.Path != "" {
		db, err = store.OpenSQLite(ctx, store.SQLiteOptions{
			Path:        cfg.Database.Path,
			BusyTimeout: cfg.Database.BusyTimeout,
		})
		if err != nil {
			slog.Error("open database", "error", err)
			os.Exit(1)
		}
		defer db.Close()
		identityService := identity.New(db.SQL(), identity.Options{})
		if err := identityService.PrepareSessionPurposeRollout(ctx); err != nil {
			slog.Error("session purpose rollout failed", "error", err)
			os.Exit(1)
		}
		if cfg.Initialization.Token != "" {
			if _, err := identityService.PrepareInitializationWithToken(ctx, cfg.Initialization.Token, cfg.Initialization.TTL); err != nil && !errors.Is(err, identity.ErrAlreadyInitialized) {
				slog.Error("prepare initialization token", "error", err)
				os.Exit(1)
			}
		}
	}

	listeners := server.NewListenerManager()
	srv := server.NewServer(cfg, db, server.WithListeners(listeners))
	if err := srv.StartupError(); err != nil {
		slog.Error("server startup gate failed", "error", err)
		os.Exit(1)
	}
	if err := srv.MarkReady(ctx); err != nil {
		slog.Error("server readiness gate failed", "error", err)
		os.Exit(1)
	}
	if db != nil {
		srv.StartJobWorker(ctx, server.JobWorkerOptions{})
	}

	if err := listeners.Start(server.EntryHTTP, cfg.HTTP.Addr, srv.Handler()); err != nil {
		slog.Error("http listener failed to bind", "addr", cfg.HTTP.Addr, "error", err)
		os.Exit(1)
	}
	slog.Info("omnora backend listening", "addr", cfg.HTTP.Addr, "db_configured", cfg.Database.Path != "")

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := listeners.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown failed", "error", err)
		os.Exit(1)
	}
}
