package workloadidentity

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func lifecycleDatabase(t *testing.T) (*repository.DeploymentStore, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("AEP_REFERENCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AEP_REFERENCE_TEST_DATABASE_URL to disposable PostgreSQL")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "workload_observer_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO deployments(id,name) VALUES('deployment-1','Observer fixture')`,
		`INSERT INTO teams(deployment_id,id,name) VALUES('deployment-1','team','Fixture team')`,
		`INSERT INTO users(id,deployment_id,username,display_name,password_hash,require_password_change,kind) VALUES('agent-1','deployment-1','agent-1','Fixture agent','test-only-unused',false,'agent')`,
		`INSERT INTO agent_profiles(deployment_id,user_id,home_team_id) VALUES('deployment-1','agent-1','team')`,
	} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	return repository.New(orm).Deployment("deployment-1"), pool
}

func TestObserverDatabaseLifecycleWithPostgres(t *testing.T) {
	store, pool := lifecycleDatabase(t)
	e, sa := employeeFixture(), accountFixture()
	o, _ := observerFixture(t, e, sa)
	o.registry = DatabaseRegistry{Store: store}
	if _, err := o.Reconcile(t.Context(), request()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ModelAccessWorkloadRegistrations(t.Context(), "employees", "worker")
	if err != nil || len(rows) != 1 || rows[0].UserID != "agent-1" {
		t.Fatal("live identity did not reach scoped DB registry", err, rows)
	}
	oldKey := rows[0].BindingKey
	if _, err := o.Reconcile(t.Context(), request()); err != nil {
		t.Fatal("unchanged live identity failed renewal", err)
	}
	// Model a recreated SA whose new UID has reached the trusted status writer.
	c := o.reader.(client.Client)
	if err := c.Delete(t.Context(), sa); err != nil {
		t.Fatal(err)
	}
	sa.UID, sa.ResourceVersion = "new-sa-uid", ""
	if err := c.Create(t.Context(), sa); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), request().NamespacedName, e); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(e.Object, "new-sa-uid", "status", "modelIdentity", "serviceAccountUid"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Reconcile(t.Context(), request()); err != nil {
		t.Fatal("replacement lost DB compare-and-swap", err)
	}
	rows, err = store.ModelAccessWorkloadRegistrations(t.Context(), "employees", "worker")
	if err != nil || len(rows) != 1 || rows[0].BindingKey == oldKey {
		t.Fatal("old binding survived replacement", err, rows)
	}
	if err := c.Delete(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Reconcile(t.Context(), request()); err != nil {
		t.Fatal(err)
	}
	var retained, live int
	if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE retired_at IS NULL) FROM model_access_workloads`).Scan(&retained, &live); err != nil || retained != 2 || live != 0 {
		t.Fatal("retirement erased history or left a live binding", retained, live, err)
	}
}
