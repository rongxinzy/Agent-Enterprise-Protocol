package repository

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
)

func TestModelAccessLicenseRevocationUpgradeWithPostgres(t *testing.T) {
	var before map[string]int64
	pool := modelAccessTestDBBefore(t, func(pool *pgxpool.Pool) {
		accessExec(t, pool, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`)
		entries, err := os.ReadDir("../db/migrations")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "056_" {
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
		accessExec(t, pool, `INSERT INTO licenses(license_id,customer_id,deployment_id,digest,key_id,issued_at,features,payload)
VALUES('fixture-revocation','fixture','access','sha256:'||repeat('a',64),'fixture',now(),ARRAY['enterprise.models'],'{}')`)
		before = accessVersions(t, pool)
		accessExec(t, pool, `UPDATE licenses SET revoked_at=now() WHERE license_id='fixture-revocation'`)
		accessChanged(t, before, accessVersions(t, pool)) // Previous release misses this predicate.
	})
	all := []string{"access/alice", "access/bea", "access/bob", "access/employee"}
	accessChanged(t, before, accessVersions(t, pool), all...)
	before = accessVersions(t, pool)
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	accessChanged(t, before, accessVersions(t, pool)) // Backfill is forward-only and idempotent.
	accessExec(t, pool, `UPDATE licenses SET revoked_at=NULL WHERE license_id='fixture-revocation'`)
	accessChanged(t, before, accessVersions(t, pool), all...)
}

func TestModelAccessSourceRejectsExplicitRevocationWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	source := readSource(t, store, "alice")
	if len(source.Licenses) != 2 {
		t.Fatal("fixture requires two active license references")
	}
	before := accessVersions(t, pool)
	sourceExec(t, pool, `UPDATE licenses SET revoked_at=now() WHERE license_id='perpetual'`)
	accessChanged(t, before, accessVersions(t, pool), "source/alice", "source/employee")
	source = readSource(t, store, "alice")
	if len(source.Licenses) != 1 || source.Licenses[0].ID != "grace" {
		t.Fatal("explicitly revoked license remained an accepted source reference")
	}
	before = accessVersions(t, pool)
	sourceExec(t, pool, `UPDATE licenses SET revoked_at=NULL WHERE license_id='perpetual'`)
	accessChanged(t, before, accessVersions(t, pool), "source/alice", "source/employee")
	if len(readSource(t, store, "alice").Licenses) != 2 {
		t.Fatal("restoring the revocation predicate did not restore source evidence")
	}
}
