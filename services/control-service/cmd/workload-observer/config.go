package main

import (
	"errors"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/util/validation"
)

type configuration struct {
	databaseURL                     string
	deployment, namespace, audience string
	freshness                       time.Duration
}

func loadConfiguration() (configuration, error) {
	var cfg configuration
	var err error
	cfg.databaseURL = os.Getenv("AEP_WORKLOAD_DATABASE_URL")
	if path := os.Getenv("AEP_WORKLOAD_DATABASE_URL_FILE"); path != "" {
		if cfg.databaseURL != "" {
			return cfg, errors.New("configure only one workload database source")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, errors.New("workload database file unavailable")
		}
		cfg.databaseURL = strings.TrimSpace(string(data))
		clear(data)
	}
	parsed, err := pgx.ParseConfig(cfg.databaseURL)
	if err != nil || cfg.databaseURL == "" {
		return cfg, errors.New("invalid workload database configuration")
	}
	environment := os.Getenv("AEP_ENVIRONMENT")
	if environment != "test" && environment != "development" {
		if environment != "production" {
			return cfg, errors.New("explicit workload environment is required")
		}
		if parsed.TLSConfig == nil || parsed.TLSConfig.InsecureSkipVerify {
			return cfg, errors.New("workload database requires verified TLS")
		}
		for _, fallback := range parsed.Fallbacks {
			if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
				return cfg, errors.New("workload database must not fall back to unverified transport")
			}
		}
	}
	cfg.deployment = os.Getenv("AEP_WORKLOAD_DEPLOYMENT_ID")
	cfg.namespace = os.Getenv("AEP_WORKLOAD_NAMESPACE")
	cfg.audience = os.Getenv("AEP_WORKLOAD_AUDIENCE")
	valid := func(value string, max int) bool {
		return value != "" && len(value) <= max && strings.IndexFunc(value, unicode.IsSpace) == -1 && strings.IndexFunc(value, unicode.IsControl) == -1
	}
	if !valid(cfg.deployment, 80) || len(validation.IsDNS1123Label(cfg.namespace)) != 0 || !valid(cfg.audience, 256) {
		return cfg, errors.New("explicit workload deployment, namespace and audience are required")
	}
	cfg.freshness, err = time.ParseDuration(os.Getenv("AEP_WORKLOAD_FRESHNESS"))
	if err != nil || cfg.freshness < 2*time.Second || cfg.freshness > time.Hour || cfg.freshness%time.Second != 0 {
		return cfg, errors.New("explicit whole-second workload freshness must be between 2s and 1h")
	}
	return cfg, nil
}
