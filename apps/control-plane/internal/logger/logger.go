package logger

import (
	"log/slog"
	"os"
)

func New(env string, levels ...string) *slog.Logger {
	var handler slog.Handler
	level := slog.LevelInfo
	if len(levels) > 0 {
		switch levels[0] {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}

	if env == "production" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	}

	return slog.New(handler)
}
