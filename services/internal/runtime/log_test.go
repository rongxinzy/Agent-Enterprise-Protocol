package runtime

import (
	"log/slog"
	"strings"
	"testing"
)

func TestConfigureLoggerValidatesFormatAndLevel(t *testing.T) {
	previous := slog.Default()
	defer slog.SetDefault(previous)
	for _, test := range []struct {
		format, level, want string
	}{
		{"json", "INFO", ""},
		{"TEXT", "warn", ""},
		{"json", "invalid", "log level"},
		{"invalid", "info", "log format"},
	} {
		err := ConfigureLogger(test.format, test.level, "test-service", "test")
		if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
			t.Fatalf("ConfigureLogger(%q, %q) error = %v", test.format, test.level, err)
		}
	}
}
