// The credential publisher delivers AEP database credentials to Kubernetes.
// It has a separate service identity; the HTTP service and gateway operator do
// not gain database keyring access or additional Kubernetes write permissions.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credentialdelivery"
)

type configuration struct {
	databaseURL string
	keyringFile string
	deployment  string
	interval    time.Duration
}

func loadConfiguration() (configuration, error) {
	var cfg configuration
	var err error
	cfg.databaseURL = os.Getenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL")
	if path := os.Getenv("AEP_CREDENTIAL_PUBLISHER_DATABASE_URL_FILE"); path != "" {
		if cfg.databaseURL != "" {
			return cfg, errors.New("configure only one publisher database source")
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return cfg, errors.New("publisher database file unavailable")
		}
		cfg.databaseURL = strings.TrimSpace(string(content))
		clear(content)
	}
	parsed, err := pgx.ParseConfig(cfg.databaseURL)
	if err != nil || cfg.databaseURL == "" {
		return cfg, errors.New("invalid publisher database configuration")
	}
	if os.Getenv("AEP_ENVIRONMENT") != "development" && os.Getenv("AEP_ENVIRONMENT") != "test" {
		if parsed.TLSConfig == nil || parsed.TLSConfig.InsecureSkipVerify {
			return cfg, errors.New("publisher database requires verified TLS in production")
		}
		for _, fallback := range parsed.Fallbacks {
			if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
				return cfg, errors.New("publisher database must not fall back to unverified transport")
			}
		}
	}
	cfg.deployment = strings.TrimSpace(os.Getenv("AEP_CREDENTIAL_PUBLISHER_DEPLOYMENT_ID"))
	cfg.keyringFile = os.Getenv("AEP_CREDENTIAL_MASTER_KEY_FILE")
	if cfg.deployment == "" || cfg.keyringFile == "" {
		return cfg, errors.New("publisher deployment and keyring file are required")
	}
	cfg.interval = 5 * time.Second
	if value := os.Getenv("AEP_CREDENTIAL_PUBLISHER_INTERVAL"); value != "" {
		cfg.interval, err = time.ParseDuration(value)
		if err != nil || cfg.interval < time.Second || cfg.interval > time.Hour {
			return cfg, errors.New("publisher interval must be between 1s and 1h")
		}
	}
	return cfg, nil
}

func run(ctx context.Context) error {
	cfg, err := loadConfiguration()
	if err != nil {
		return err
	}
	provider, _, err := credential.NewProvider("", cfg.keyringFile)
	if err != nil {
		return errors.New("publisher keyring unavailable")
	}
	db, err := gorm.Open(postgres.Open(cfg.databaseURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return errors.New("publisher database initialization failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("publisher database unavailable")
	}
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		return errors.New("publisher Kubernetes configuration unavailable")
	}
	kubeConfig.Timeout = 10 * time.Second
	kubeConfig.UserAgent = "aep-credential-publisher"
	client, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return errors.New("publisher Kubernetes client unavailable")
	}
	publisher, err := credentialdelivery.New(credentialdelivery.DatabaseSource{DB: db, Deployment: cfg.deployment}, credential.NewSealer(provider), client.CoreV1().Secrets(credentialdelivery.Namespace), cfg.deployment, cfg.interval)
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: ":8095",
		LeaderElection:         true, LeaderElectionNamespace: "aep-system",
		LeaderElectionID: "aep-credential-publisher", LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return errors.New("publisher manager initialization failed")
	}
	if err := mgr.Add(publisher); err != nil {
		return errors.New("publisher worker registration failed")
	}
	if err := mgr.AddHealthzCheck("live", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("delivery", func(_ *http.Request) error { return publisher.StatusError() }); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	if err := run(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "credential publisher stopped")
		os.Exit(1)
	}
}
