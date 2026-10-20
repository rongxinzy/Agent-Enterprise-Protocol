// The workload observer is a separately enabled trusted lifecycle writer. It
// reads no provider credentials and gains no Kubernetes Secret/TokenReview write
// permissions. Business authorization remains in the role/publication pipeline.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/workloadidentity"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func run(ctx context.Context) error {
	cfg, err := loadConfiguration()
	if err != nil {
		return err
	}
	db, err := gorm.Open(postgres.Open(cfg.databaseURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		return errors.New("workload database initialization failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("workload database unavailable")
	}
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = sqlDB.PingContext(pingCtx)
	cancel()
	if err != nil {
		return errors.New("workload database connection failed")
	}
	kube, err := ctrl.GetConfig()
	if err != nil {
		return errors.New("workload Kubernetes configuration unavailable")
	}
	kube.Timeout = 10 * time.Second
	kube.UserAgent = "aep-workload-observer"
	mgr, err := ctrl.NewManager(kube, ctrl.Options{
		Cache:   cache.Options{DefaultNamespaces: map[string]cache.Config{cfg.namespace: {}}},
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: ":8096",
		LeaderElection: true, LeaderElectionNamespace: "aep-system", LeaderElectionID: "aep-workload-observer",
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return errors.New("workload manager initialization failed")
	}
	registry := workloadidentity.DatabaseRegistry{Store: repository.New(db).Deployment(cfg.deployment)}
	if err := workloadidentity.Setup(mgr, registry, cfg.deployment, cfg.namespace, cfg.audience, cfg.freshness); err != nil {
		return errors.New("workload observer registration failed")
	}
	// Dependency readiness does not imply model authorization or adoption. The
	// publication ledger owns data-plane evidence; liveness stays independent.
	if err := mgr.AddHealthzCheck("live", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("dependencies", func(request *http.Request) error {
		checkCtx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if err := sqlDB.PingContext(checkCtx); err != nil {
			return errors.New("workload database unavailable")
		}
		if !mgr.GetCache().WaitForCacheSync(checkCtx) {
			return errors.New("workload informer not synchronized")
		}
		return nil
	}); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	if err := run(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "workload observer stopped")
		os.Exit(1)
	}
}
