package logger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestNewSelectsFormatAndLevel(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		levels      []string
		handlerType string
		enabled     slog.Level
		disabled    slog.Level
	}{
		{name: "development defaults to info text", environment: "development", handlerType: "TextHandler", enabled: slog.LevelInfo, disabled: slog.LevelDebug},
		{name: "production uses JSON", environment: "production", handlerType: "JSONHandler", enabled: slog.LevelInfo, disabled: slog.LevelDebug},
		{name: "debug", environment: "development", levels: []string{"debug"}, handlerType: "TextHandler", enabled: slog.LevelDebug, disabled: slog.LevelDebug - 1},
		{name: "warn", environment: "development", levels: []string{"warn"}, handlerType: "TextHandler", enabled: slog.LevelWarn, disabled: slog.LevelInfo},
		{name: "error", environment: "development", levels: []string{"error"}, handlerType: "TextHandler", enabled: slog.LevelError, disabled: slog.LevelWarn},
		{name: "unknown falls back to info", environment: "development", levels: []string{"trace"}, handlerType: "TextHandler", enabled: slog.LevelInfo, disabled: slog.LevelDebug},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := New(test.environment, test.levels...)
			handlerName := fmt.Sprintf("%T", log.Handler())
			if !strings.Contains(handlerName, test.handlerType) {
				t.Fatalf("handler = %s, want %s", handlerName, test.handlerType)
			}
			if !log.Enabled(context.Background(), test.enabled) {
				t.Fatalf("level %s should be enabled", test.enabled)
			}
			if log.Enabled(context.Background(), test.disabled) {
				t.Fatalf("level %s should be disabled", test.disabled)
			}
		})
	}
}
