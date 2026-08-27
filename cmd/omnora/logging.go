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
	slog.SetDefault(newLogger(cfg.Format, level, output))
}

func newLogger(format string, level slog.Leveler, output io.Writer) *slog.Logger {
	options := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(output, options))
	}
	return slog.New(slog.NewTextHandler(output, options))
}
