package repository

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
)

func TestWorkloadPodObservationUpgradeWithPostgres(t *testing.T) {
	var before map[string]int64
	pool := modelAccessTestDBBefore(t, func(pool *pgxpool.Pool) {
		accessExec(t, pool, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`)
		entries, err := os.ReadDir("../db/migrations")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "057_" {
				continue
			}
			data, err := os.ReadFile("../db/migrations/" + entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			accessExec(t, pool, string(data))
			accessExec(t, pool, `INSERT INTO schema_migrations(version) VALUES($1)`, entry.Name())
		}
		seedAccess(t, pool)
		accessExec(t, pool, `INSERT INTO model_access_workloads(deployment_id,binding_key,user_id,namespace,employee_name,
employee_uid,service_account_name,service_account_uid,audience,verified_at,verified_until)
VALUES('access',repeat('a',64),'employee','employees','worker','employee-uid','worker-model','sa-uid','gateway',now(),now()+interval '5 minutes')`)
		before = accessVersions(t, pool)
	})
	accessChanged(t, before, accessVersions(t, pool), "access/employee")
	var empty bool
	if err := pool.QueryRow(t.Context(), `SELECT pod_uids='[]'::jsonb FROM model_access_workloads`).Scan(&empty); err != nil || !empty {
		t.Fatal("upgrade fabricated positive Pod evidence", err)
	}
	before = accessVersions(t, pool)
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	accessChanged(t, before, accessVersions(t, pool))
	accessExec(t, pool, `UPDATE model_access_workloads SET pod_uids='["pod-a"]'::jsonb`)
	accessChanged(t, before, accessVersions(t, pool), "access/employee")
	before = accessVersions(t, pool)
	accessExec(t, pool, `UPDATE model_access_workloads SET pod_uids='["pod-a"]'::jsonb,verified_until=verified_until+interval '1 minute'`)
	accessChanged(t, before, accessVersions(t, pool))
}
