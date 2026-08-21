package gateway

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Address        string
	UpstreamURL    string
	JWKSURL        string
	Issuer         string
	JWKSTTL        time.Duration
	RequestLimit   int64
	RequestTimeout time.Duration
}

func LoadConfig() Config {
	return Config{
		Address:        value("AEP_GATEWAY_ADDRESS", ":8090"),
		UpstreamURL:    value("AEP_GATEWAY_UPSTREAM_URL", "http://localhost:8080"),
		JWKSURL:        value("AEP_GATEWAY_JWKS_URL", "http://localhost:8080/.well-known/jwks.json"),
		Issuer:         value("AEP_GATEWAY_ISSUER", "http://localhost:8080"),
		JWKSTTL:        duration("AEP_GATEWAY_JWKS_TTL", 5*time.Minute),
		RequestLimit:   integer("AEP_GATEWAY_REQUEST_LIMIT", 2<<20),
		RequestTimeout: duration("AEP_GATEWAY_JWKS_TIMEOUT", 2*time.Second),
	}
}

func value(key, fallback string) string {
	if current := os.Getenv(key); current != "" {
		return current
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(os.Getenv(key))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func integer(key string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
