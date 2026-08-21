package runtime

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
)

func ConfigureLogger(format, level, service, environment string) error {
	var parsedLevel slog.Level
	if err := parsedLevel.UnmarshalText([]byte(strings.ToLower(level))); err != nil {
		return errors.New("log level must be debug, info, warn, or error")
	}
	options := &slog.HandlerOptions{Level: parsedLevel}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, options)
	case "text":
		handler = slog.NewTextHandler(os.Stdout, options)
	default:
		return errors.New("log format must be json or text")
	}
	slog.SetDefault(slog.New(handler).With("service", service, "environment", environment))
	return nil
}

func NewTestLogger(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, nil))
}
