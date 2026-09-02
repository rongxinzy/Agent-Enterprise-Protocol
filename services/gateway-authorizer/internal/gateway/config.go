package gateway

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Environment           string
	LogFormat             string
	LogLevel              string
	Address               string
	UpstreamURL           string
	JWKSURL               string
	Issuer                string
	JWKSTTL               time.Duration
	RequestLimit          int64
	RequestTimeout        time.Duration
	UpstreamHeaderTimeout time.Duration
	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPShutdownTimeout   time.Duration
	HTTPMaxHeaderBytes    int
}

func LoadConfig() (Config, error) {
	environment := value("AEP_ENVIRONMENT", "development")
	defaultLogFormat := "text"
	if environment == "production" {
		defaultLogFormat = "json"
	}
	cfg := Config{
		Environment: environment,
		LogFormat:   value("AEP_LOG_FORMAT", defaultLogFormat),
		LogLevel:    value("AEP_LOG_LEVEL", "info"),
		Address:     value("AEP_GATEWAY_ADDRESS", ":8090"),
		UpstreamURL: value("AEP_GATEWAY_UPSTREAM_URL", "http://localhost:8080"),
		JWKSURL:     value("AEP_GATEWAY_JWKS_URL", "http://localhost:8080/.well-known/jwks.json"),
		Issuer:      value("AEP_GATEWAY_ISSUER", "http://localhost:8080"),
	}
	var err error
	durations := []struct {
		key      string
		fallback time.Duration
		target   *time.Duration
		zeroOK   bool
	}{
		{"AEP_GATEWAY_JWKS_TTL", 5 * time.Minute, &cfg.JWKSTTL, false},
		{"AEP_GATEWAY_JWKS_TIMEOUT", 2 * time.Second, &cfg.RequestTimeout, false},
		{"AEP_GATEWAY_UPSTREAM_HEADER_TIMEOUT", 15 * time.Second, &cfg.UpstreamHeaderTimeout, false},
		{"AEP_GATEWAY_HTTP_READ_HEADER_TIMEOUT", 5 * time.Second, &cfg.HTTPReadHeaderTimeout, false},
		{"AEP_GATEWAY_HTTP_READ_TIMEOUT", 15 * time.Second, &cfg.HTTPReadTimeout, false},
		{"AEP_GATEWAY_HTTP_WRITE_TIMEOUT", 0, &cfg.HTTPWriteTimeout, true},
		{"AEP_GATEWAY_HTTP_IDLE_TIMEOUT", 60 * time.Second, &cfg.HTTPIdleTimeout, false},
		{"AEP_GATEWAY_HTTP_SHUTDOWN_TIMEOUT", 10 * time.Second, &cfg.HTTPShutdownTimeout, false},
	}
	for _, item := range durations {
		if *item.target, err = duration(item.key, item.fallback, item.zeroOK); err != nil {
			return Config{}, err
		}
	}
	if cfg.RequestLimit, err = integer64("AEP_GATEWAY_REQUEST_LIMIT", 2<<20); err != nil {
		return Config{}, err
	}
	if cfg.HTTPMaxHeaderBytes, err = integer("AEP_GATEWAY_HTTP_MAX_HEADER_BYTES", 1<<20); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if cfg.Environment != "development" && cfg.Environment != "test" && cfg.Environment != "production" {
		return errors.New("AEP_ENVIRONMENT must be development, test, or production")
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return errors.New("AEP_LOG_FORMAT must be text or json")
	}
	if cfg.LogLevel != "debug" && cfg.LogLevel != "info" && cfg.LogLevel != "warn" && cfg.LogLevel != "error" {
		return errors.New("AEP_LOG_LEVEL must be debug, info, warn, or error")
	}
	for _, endpoint := range []struct {
		key   string
		value string
	}{
		{"AEP_GATEWAY_UPSTREAM_URL", cfg.UpstreamURL},
		{"AEP_GATEWAY_JWKS_URL", cfg.JWKSURL},
		{"AEP_GATEWAY_ISSUER", cfg.Issuer},
	} {
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%s must be an absolute HTTP URL", endpoint.key)
		}
	}
	return nil
}

func value(key, fallback string) string {
	if current := os.Getenv(key); current != "" {
		return current
	}
	return fallback
}

func duration(key string, fallback time.Duration, zeroOK bool) (time.Duration, error) {
	current := os.Getenv(key)
	if current == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(current)
	if err != nil || parsed < 0 || (!zeroOK && parsed == 0) {
		return 0, fmt.Errorf("%s must be a valid positive duration", key)
	}
	return parsed, nil
}

func integer64(key string, fallback int64) (int64, error) {
	current := os.Getenv(key)
	if current == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(current, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func integer(key string, fallback int) (int, error) {
	parsed, err := integer64(key, int64(fallback))
	return int(parsed), err
}
