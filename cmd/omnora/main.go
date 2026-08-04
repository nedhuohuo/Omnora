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

	listeners := server.NewListenerManager()
	srv := server.NewServer(cfg, db, server.WithListeners(listeners))
	lanHandler := srv.HandlerFor(server.EntryLAN)
	proxyHandler := srv.HandlerFor(server.EntryProxy)

	if err := listeners.Start(server.EntryLAN, cfg.HTTP.Addr, lanHandler); err != nil {
		slog.Error("lan http listener failed to bind", "addr", cfg.HTTP.Addr, "error", err)
		os.Exit(1)
	}
	slog.Info("omnora backend listening", "addr", cfg.HTTP.Addr, "db_configured", cfg.Database.Path != "")

	if enabled, bindAddr := srv.ProxyEntryState(); enabled {
		if bindAddr == "" {
			bindAddr = cfg.HTTP.ProxyHTTPSListen
		}
		// A proxy bind failure is non-fatal: the LAN entry remains the
		// management surface and the admin can reconcile via the API.
		if err := listeners.Start(server.EntryProxy, bindAddr, proxyHandler); err != nil {
			slog.Error("proxy https listener failed to bind", "addr", bindAddr, "error", err)
		} else {
			slog.Info("proxy https listener started", "addr", bindAddr)
		}
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := listeners.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown failed", "error", err)
		os.Exit(1)
	}
}
