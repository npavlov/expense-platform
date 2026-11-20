// Package logging configures structured JSON logs.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON logger tagged with the service name and sets it as the
// default, so packages that log through slog share the configuration.
// LOG_LEVEL may be debug, info, warn or error.
func New(service string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	l := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", service)
	slog.SetDefault(l)
	return l
}
