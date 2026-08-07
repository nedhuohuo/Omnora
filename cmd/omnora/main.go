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
	"omnora/internal/recovery"
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
	recoveryOnly := false
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

		// An offline recovery worker (cmd/omnora-recovery) may have replaced
		// this SQLite file with an older backup snapshot, which wipes out the
		// restore_requests/recovery_control rows written when the restore was
		// requested. Rehydrate them from the durable sidecar journal before
		// making any decision based on recovery state.
		recoveryCoordinator := recovery.NewCoordinator(db.SQL(), recovery.WithJournalPath(recovery.DefaultJournalPath(cfg.Database.Path)))
		if rehydrated, err := recoveryCoordinator.RehydrateFromJournal(ctx); err != nil {
			slog.Error("recovery journal rehydrate failed", "error", err)
			os.Exit(1)
		} else if rehydrated {
			slog.Warn("recovery journal rehydrated a restore request lost by a database replacement")
		}
		control, err := recoveryCoordinator.Control(ctx)
		if err != nil {
			slog.Error("read recovery control", "error", err)
			os.Exit(1)
		}
		if control.State != recovery.StateNormal {
			// Do not fail closed with os.Exit here: that would crash-loop the
			// process forever with no path forward. Instead this process
			// enters a recovery-only mode that serves /healthz and /readyz
			// (which already reports 503 while recovery.state != normal) but
			// skips MarkReady and the job worker, leaving an operator free to
			// run `omnora-recovery` to advance the state. A later restart
			// with recovery.state == normal resumes ordinary startup.
			recoveryOnly = true
			slog.Warn("recovery is in progress; starting in recovery-only mode", "state", control.State, "request_id", control.RequestID)
		}

		if !recoveryOnly {
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
	}

	listeners := server.NewListenerManager()
	srv := server.NewServer(cfg, db, server.WithListeners(listeners))
	if err := srv.StartupError(); err != nil {
		slog.Error("server startup gate failed", "error", err)
		os.Exit(1)
	}
	if !recoveryOnly {
		if err := srv.MarkReady(ctx); err != nil {
			slog.Error("server readiness gate failed", "error", err)
			os.Exit(1)
		}
		if db != nil {
			srv.StartJobWorker(ctx, server.JobWorkerOptions{})
		}
	}

	if err := listeners.Start(server.EntryHTTP, cfg.HTTP.Addr, srv.Handler()); err != nil {
		slog.Error("http listener failed to bind", "addr", cfg.HTTP.Addr, "error", err)
		os.Exit(1)
	}
	slog.Info("omnora backend listening", "addr", cfg.HTTP.Addr, "db_configured", cfg.Database.Path != "", "recovery_only", recoveryOnly)

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case <-srv.ShutdownRequested():
		// A restore request was accepted: this process must stop serving
		// business routes before an offline recovery worker replaces the
		// database out from under it.
		slog.Warn("restore request triggered a controlled shutdown; run omnora-recovery to complete the offline restore")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := listeners.Shutdown(shutdownCtx); err != nil {
		slog.Error("http shutdown failed", "error", err)
		os.Exit(1)
	}
}
