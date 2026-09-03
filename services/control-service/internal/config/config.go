package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDatabaseURL   = "postgres://aep:aep@localhost:5432/aep?sslmode=disable"
	defaultAdminPassword = "change-this-admin-password"
)

type Config struct {
	Environment               string
	LogFormat                 string
	LogLevel                  string
	EnableMockFederatedAuth   bool
	Address                   string
	HTTPReadHeaderTimeout     time.Duration
	HTTPReadTimeout           time.Duration
	HTTPWriteTimeout          time.Duration
	HTTPIdleTimeout           time.Duration
	HTTPShutdownTimeout       time.Duration
	HTTPMaxHeaderBytes        int
	LoginFailureLimit         int
	LoginFailureWindow        time.Duration
	LoginBackoffBase          time.Duration
	LoginBackoffMax           time.Duration
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
	DataPlaneReconcilerToken  string
	GatewayLicenseStatusToken string
	CredentialMasterKeyBase64 string
	CredentialMasterKeyFile   string
	LicenseTrustedKeys        map[string]string
	LicenseTrustedKeysFile    string
	LicenseFile               string
	LicenseDeploymentID       string
	LicenseCustomerID         string
	LicenseEnterpriseID       string
	RefreshTTL                time.Duration
	BootstrapEnterpriseID     string
	BootstrapEnterpriseName   string
	BootstrapAdminUsername    string
	BootstrapAdminPassword    string
	BootstrapAdminDisplayName string
}

func Load() (Config, error) {
	environment := value("AEP_ENVIRONMENT", "development")
	defaultLogFormat := "text"
	if environment == "production" {
		defaultLogFormat = "json"
	}
	databaseURL, err := secret("AEP_DATABASE_URL", defaultDatabaseURL)
	if err != nil {
		return Config{}, err
	}
	minioAccessKey, err := secret("AEP_MINIO_ACCESS_KEY", "minioadmin")
	if err != nil {
		return Config{}, err
	}
	minioSecretKey, err := secret("AEP_MINIO_SECRET_KEY", "minioadmin")
	if err != nil {
		return Config{}, err
	}
	signingKey, err := secret("AEP_SIGNING_KEY_BASE64", "")
	if err != nil {
		return Config{}, err
	}
	credentialKey, err := secret("AEP_CREDENTIAL_MASTER_KEY_BASE64", "")
	if err != nil {
		return Config{}, err
	}
	adminPassword, err := secret("AEP_BOOTSTRAP_ADMIN_PASSWORD", defaultAdminPassword)
	if err != nil {
		return Config{}, err
	}
	dataPlaneToken, err := secret("AEP_DATA_PLANE_RECONCILER_TOKEN", "")
	if err != nil {
		return Config{}, err
	}
	gatewayLicenseToken, err := secret("AEP_GATEWAY_LICENSE_STATUS_TOKEN", "")
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Environment:               environment,
		LogFormat:                 value("AEP_LOG_FORMAT", defaultLogFormat),
		LogLevel:                  value("AEP_LOG_LEVEL", "info"),
		Address:                   value("AEP_ADDRESS", ":8080"),
		DatabaseURL:               databaseURL,
		MinioEndpoint:             value("AEP_MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey:            minioAccessKey,
		MinioSecretKey:            minioSecretKey,
		MinioBucket:               value("AEP_MINIO_BUCKET", "aep-skills"),
		Issuer:                    value("AEP_ISSUER", "http://localhost:8080"),
		SigningKeyBase64:          signingKey,
		ModelGatewayBaseURL:       os.Getenv("AEP_MODEL_GATEWAY_BASE_URL"),
		DataPlaneReconcilerToken:  dataPlaneToken,
		GatewayLicenseStatusToken: gatewayLicenseToken,
		CredentialMasterKeyBase64: credentialKey,
		CredentialMasterKeyFile:   os.Getenv("AEP_CREDENTIAL_MASTER_KEY_FILE"),
		LicenseTrustedKeysFile:    os.Getenv("AEP_LICENSE_TRUSTED_KEYS_FILE"),
		LicenseFile:               os.Getenv("AEP_LICENSE_FILE"),
		LicenseDeploymentID:       os.Getenv("AEP_LICENSE_DEPLOYMENT_ID"),
		LicenseCustomerID:         os.Getenv("AEP_LICENSE_CUSTOMER_ID"),
		LicenseEnterpriseID:       os.Getenv("AEP_LICENSE_ENTERPRISE_ID"),
		BootstrapEnterpriseID:     value("AEP_BOOTSTRAP_ENTERPRISE_ID", "demo"),
		BootstrapEnterpriseName:   value("AEP_BOOTSTRAP_ENTERPRISE_NAME", "Demo Enterprise"),
		BootstrapAdminUsername:    value("AEP_BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminPassword:    adminPassword,
		BootstrapAdminDisplayName: value("AEP_BOOTSTRAP_ADMIN_DISPLAY_NAME", "AEP Administrator"),
	}
	if cfg.LicenseTrustedKeys, err = loadTrustedKeys(cfg.LicenseTrustedKeysFile); err != nil {
		return Config{}, err
	}
	if cfg.EnableMockFederatedAuth, err = boolean("AEP_ENABLE_MOCK_FEDERATED_AUTH", environment != "production"); err != nil {
		return Config{}, err
	}
	if cfg.MinioSecure, err = boolean("AEP_MINIO_SECURE", false); err != nil {
		return Config{}, err
	}
	durations := []struct {
		key      string
		fallback time.Duration
		target   *time.Duration
		zeroOK   bool
	}{
		{"AEP_ACCESS_TTL", 15 * time.Minute, &cfg.AccessTTL, false},
		{"AEP_MODEL_ACCESS_TTL", 15 * time.Minute, &cfg.ModelAccessTTL, false},
		{"AEP_REFRESH_TTL", 30 * 24 * time.Hour, &cfg.RefreshTTL, false},
		{"AEP_HTTP_READ_HEADER_TIMEOUT", 5 * time.Second, &cfg.HTTPReadHeaderTimeout, false},
		{"AEP_HTTP_READ_TIMEOUT", 15 * time.Second, &cfg.HTTPReadTimeout, false},
		{"AEP_HTTP_WRITE_TIMEOUT", 30 * time.Second, &cfg.HTTPWriteTimeout, true},
		{"AEP_HTTP_IDLE_TIMEOUT", 60 * time.Second, &cfg.HTTPIdleTimeout, false},
		{"AEP_HTTP_SHUTDOWN_TIMEOUT", 10 * time.Second, &cfg.HTTPShutdownTimeout, false},
		{"AEP_LOGIN_FAILURE_WINDOW", 15 * time.Minute, &cfg.LoginFailureWindow, false},
		{"AEP_LOGIN_BACKOFF_BASE", 30 * time.Second, &cfg.LoginBackoffBase, false},
		{"AEP_LOGIN_BACKOFF_MAX", 15 * time.Minute, &cfg.LoginBackoffMax, false},
	}
	for _, item := range durations {
		if *item.target, err = duration(item.key, item.fallback, item.zeroOK); err != nil {
			return Config{}, err
		}
	}
	if cfg.HTTPMaxHeaderBytes, err = integer("AEP_HTTP_MAX_HEADER_BYTES", 1<<20); err != nil {
		return Config{}, err
	}
	if cfg.LoginFailureLimit, err = integer("AEP_LOGIN_FAILURE_LIMIT", 5); err != nil {
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
	if _, err := absoluteURL("AEP_DATABASE_URL", cfg.DatabaseURL, "postgres", "postgresql"); err != nil {
		return err
	}
	if _, err := absoluteURL("AEP_ISSUER", cfg.Issuer, "http", "https"); err != nil {
		return err
	}
	if cfg.ModelGatewayBaseURL != "" {
		if _, err := absoluteURL("AEP_MODEL_GATEWAY_BASE_URL", cfg.ModelGatewayBaseURL, "http", "https"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.MinioEndpoint) == "" || strings.TrimSpace(cfg.MinioBucket) == "" {
		return errors.New("AEP_MINIO_ENDPOINT and AEP_MINIO_BUCKET must not be empty")
	}
	if cfg.LoginBackoffMax < cfg.LoginBackoffBase {
		return errors.New("AEP_LOGIN_BACKOFF_MAX must be greater than or equal to AEP_LOGIN_BACKOFF_BASE")
	}
	if cfg.Environment == "production" {
		switch {
		case cfg.EnableMockFederatedAuth:
			return errors.New("AEP_ENABLE_MOCK_FEDERATED_AUTH must be false in production")
		case cfg.SigningKeyBase64 == "":
			return errors.New("AEP_SIGNING_KEY_BASE64 or AEP_SIGNING_KEY_BASE64_FILE is required in production")
		case cfg.DatabaseURL == defaultDatabaseURL:
			return errors.New("the development AEP_DATABASE_URL is forbidden in production")
		case cfg.MinioAccessKey == "minioadmin" || cfg.MinioSecretKey == "minioadmin":
			return errors.New("the development MinIO credentials are forbidden in production")
		case cfg.BootstrapAdminPassword == defaultAdminPassword || len(cfg.BootstrapAdminPassword) < 12:
			return errors.New("a non-default bootstrap administrator password of at least 12 characters is required in production")
		case len(cfg.LicenseTrustedKeys) == 0:
			return errors.New("AEP_LICENSE_TRUSTED_KEYS_FILE with at least one key is required in production")
		case strings.TrimSpace(cfg.LicenseDeploymentID) == "":
			return errors.New("AEP_LICENSE_DEPLOYMENT_ID is required in production")
		case strings.TrimSpace(cfg.LicenseCustomerID) == "":
			return errors.New("AEP_LICENSE_CUSTOMER_ID is required in production")
		case strings.TrimSpace(cfg.LicenseEnterpriseID) == "":
			return errors.New("AEP_LICENSE_ENTERPRISE_ID is required in production")
		case strings.TrimSpace(cfg.LicenseFile) == "":
			return errors.New("AEP_LICENSE_FILE is required in production")
		}
	}
	return nil
}

func loadTrustedKeys(file string) (map[string]string, error) {
	if strings.TrimSpace(file) == "" {
		return map[string]string{}, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read AEP_LICENSE_TRUSTED_KEYS_FILE: %w", err)
	}
	var keys map[string]string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("parse AEP_LICENSE_TRUSTED_KEYS_FILE: %w", err)
	}
	keyIDPattern := regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	keyPattern := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	if len(keys) == 0 {
		return nil, errors.New("AEP_LICENSE_TRUSTED_KEYS_FILE must contain at least one key")
	}
	for keyID, key := range keys {
		if !keyIDPattern.MatchString(keyID) || !keyPattern.MatchString(key) {
			return nil, errors.New("AEP_LICENSE_TRUSTED_KEYS_FILE contains an invalid key")
		}
	}
	return keys, nil
}

func value(key, fallback string) string {
	if current := os.Getenv(key); current != "" {
		return current
	}
	return fallback
}

func secret(key, fallback string) (string, error) {
	direct, file := os.Getenv(key), os.Getenv(key+"_FILE")
	if direct != "" && file != "" {
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", key, key)
	}
	if file == "" {
		if direct != "" {
			return direct, nil
		}
		return fallback, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", key, err)
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("%s_FILE exceeds 1 MiB", key)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func boolean(key string, fallback bool) (bool, error) {
	current := os.Getenv(key)
	if current == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(current)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
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

func integer(key string, fallback int) (int, error) {
	current := os.Getenv(key)
	if current == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(current)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func absoluteURL(key, raw string, schemes ...string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute URL", key)
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return parsed, nil
		}
	}
	return nil, fmt.Errorf("%s must use one of these schemes: %s", key, strings.Join(schemes, ", "))
}
