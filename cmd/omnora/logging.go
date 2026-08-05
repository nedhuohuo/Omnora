package main

import (
	"io"
	"log/slog"

	"omnora/internal/config"
)

func configureLogger(cfg config.LogConfig, output io.Writer) {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	options := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(output, options)))
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(output, options)))
}
