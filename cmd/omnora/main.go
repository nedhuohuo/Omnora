package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"omnora/internal/config"
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
		if cfg.Initialization.Token != "" {
			identityService := identity.New(db.SQL(), identity.Options{})
			if _, err := identityService.PrepareInitializationWithToken(ctx, cfg.Initialization.Token, cfg.Initialization.TTL); err != nil && !errors.Is(err, identity.ErrAlreadyInitialized) {
				slog.Error("prepare initialization token", "error", err)
				os.Exit(1)
			}
		}
	}

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           server.New(cfg, db),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("omnora backend listening", "addr", cfg.HTTP.Addr, "db_configured", cfg.Database.Path != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown failed", "error", err)
		os.Exit(1)
	}
}
