package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Address                   string
	DatabaseURL               string
	MinioEndpoint             string
	MinioAccessKey            string
	MinioSecretKey            string
	MinioBucket               string
	MinioSecure               bool
	Issuer                    string
	SigningKeyBase64          string
	AccessTTL                 time.Duration
	ModelAccessTTL            time.Duration
	ModelGatewayBaseURL       string
	CredentialMasterKeyBase64 string
	CredentialMasterKeyFile   string
	RefreshTTL                time.Duration
	BootstrapEnterpriseID     string
	BootstrapEnterpriseName   string
	BootstrapAdminUsername    string
	BootstrapAdminPassword    string
	BootstrapAdminDisplayName string
}

func Load() Config {
	return Config{
		Address:                   value("AEP_ADDRESS", ":8080"),
		DatabaseURL:               value("AEP_DATABASE_URL", "postgres://aep:aep@localhost:5432/aep?sslmode=disable"),
		MinioEndpoint:             value("AEP_MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey:            value("AEP_MINIO_ACCESS_KEY", "minioadmin"),
		MinioSecretKey:            value("AEP_MINIO_SECRET_KEY", "minioadmin"),
		MinioBucket:               value("AEP_MINIO_BUCKET", "aep-skills"),
		MinioSecure:               boolean("AEP_MINIO_SECURE", false),
		Issuer:                    value("AEP_ISSUER", "http://localhost:8080"),
		SigningKeyBase64:          os.Getenv("AEP_SIGNING_KEY_BASE64"),
		AccessTTL:                 duration("AEP_ACCESS_TTL", 15*time.Minute),
		ModelAccessTTL:            duration("AEP_MODEL_ACCESS_TTL", 15*time.Minute),
		ModelGatewayBaseURL:       os.Getenv("AEP_MODEL_GATEWAY_BASE_URL"),
		CredentialMasterKeyBase64: os.Getenv("AEP_CREDENTIAL_MASTER_KEY_BASE64"),
		CredentialMasterKeyFile:   os.Getenv("AEP_CREDENTIAL_MASTER_KEY_FILE"),
		RefreshTTL:                duration("AEP_REFRESH_TTL", 30*24*time.Hour),
		BootstrapEnterpriseID:     value("AEP_BOOTSTRAP_ENTERPRISE_ID", "demo"),
		BootstrapEnterpriseName:   value("AEP_BOOTSTRAP_ENTERPRISE_NAME", "Demo Enterprise"),
		BootstrapAdminUsername:    value("AEP_BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminPassword:    value("AEP_BOOTSTRAP_ADMIN_PASSWORD", "change-this-admin-password"),
		BootstrapAdminDisplayName: value("AEP_BOOTSTRAP_ADMIN_DISPLAY_NAME", "AEP Administrator"),
	}
}

func value(key, fallback string) string {
	if current := os.Getenv(key); current != "" {
		return current
	}
	return fallback
}

func boolean(key string, fallback bool) bool {
	current := os.Getenv(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(current)
	if err != nil {
		return fallback
	}
	return parsed
}

func duration(key string, fallback time.Duration) time.Duration {
	current := os.Getenv(key)
	if current == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(current)
	if err != nil {
		return fallback
	}
	return parsed
}
