package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"omnora/internal/config"
)

func TestConfigureLoggerJSONAndLevel(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	configureLogger(config.LogConfig{Format: "json", Level: "debug"}, &buf)
	slog.Debug("debug-visible", "request_id", "req-1")

	output := buf.String()
	if !strings.Contains(output, `"msg":"debug-visible"`) {
		t.Fatalf("json log missing message: %s", output)
	}
	if !strings.Contains(output, `"request_id":"req-1"`) {
		t.Fatalf("json log missing structured request_id: %s", output)
	}
}

func TestConfigureLoggerLevelFilters(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	var buf bytes.Buffer
	configureLogger(config.LogConfig{Format: "text", Level: "warn"}, &buf)
	slog.Info("hidden")
	slog.Warn("visible")

	output := buf.String()
	if strings.Contains(output, "hidden") {
		t.Fatalf("info log should be filtered at warn level: %s", output)
	}
	if !strings.Contains(output, "visible") {
		t.Fatalf("warn log missing: %s", output)
	}
}
