package repository

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
)

// SQL mocks cannot verify trigger ordering, cascade capture or transaction
// rollback. Like the reference-lifecycle suite, this requires an explicitly
// supplied disposable server and owns only its randomly named database.
func modelAccessTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return modelAccessTestDBBefore(t, nil)
}

func modelAccessTestDBBefore(t *testing.T, prepare func(*pgxpool.Pool)) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("AEP_REFERENCE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AEP_REFERENCE_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "access_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP DATABASE `+name); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if prepare != nil {
		prepare(pool)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func accessExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func accessVersions(t *testing.T, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT deployment_id||'/'||user_id,version FROM model_access_changes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := map[string]int64{}
	for rows.Next() {
		var key string
		var version int64
		if err := rows.Scan(&key, &version); err != nil {
			t.Fatal(err)
		}
		values[key] = version
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func accessChanged(t *testing.T, before, after map[string]int64, want ...string) {
	t.Helper()
	got := []string{}
	for key, version := range after {
		if version > before[key] {
			got = append(got, key)
		}
		if version < before[key] {
			t.Fatalf("signal regressed: %s", key)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("changed principals = %v, want %v", got, want)
	}
}

func seedAccess(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	accessExec(t, pool, `INSERT INTO deployments(id,name) VALUES('access','Test'),('other','Other')`)
	accessExec(t, pool, `INSERT INTO roles(deployment_id,id,name) VALUES('access','role','Role'),('access','unused','Unused'),('other','role','Role')`)
	accessExec(t, pool, `INSERT INTO teams(deployment_id,id,name) VALUES('access','team','Team'),('other','team','Team')`)
	for _, id := range []string{"alice", "bea", "employee", "bob", "foreign"} {
		dep, kind := "access", "human"
		if id == "foreign" {
			dep = "other"
		}
		if id == "employee" {
			kind = "agent"
		}
		accessExec(t, pool, `INSERT INTO users(id,deployment_id,username,display_name,password_hash,kind) VALUES($1,$2,$1,$1,'test-only-unused',$3)`, id, dep, kind)
	}
	accessExec(t, pool, `INSERT INTO user_role_bindings(deployment_id,user_id,role_id) VALUES('access','alice','role'),('other','foreign','role')`)
	accessExec(t, pool, `INSERT INTO user_team_bindings(deployment_id,user_id,team_id) VALUES('access','bea','team'),('access','employee','team'),('other','foreign','team')`)
	accessExec(t, pool, `INSERT INTO team_default_roles(deployment_id,team_id,role_id) VALUES('access','team','role'),('other','team','role')`)
	accessExec(t, pool, `INSERT INTO models(deployment_id,id,display_name,source_type,protocol) VALUES('access','chat','Chat','gateway','openai-compatible'),('other','chat','Chat','gateway','openai-compatible')`)
	accessExec(t, pool, `INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id) VALUES('grant','access','chat','role','role'),('foreign-grant','other','chat','role','role')`)
	accessExec(t, pool, `INSERT INTO user_sessions(session_id,deployment_id,user_id,topic) VALUES('session','access','alice','test')`)
	accessExec(t, pool, `INSERT INTO agent_profiles(deployment_id,user_id,home_team_id) VALUES('access','employee','team')`)
}

func TestModelAccessChangeDependenciesWithPostgres(t *testing.T) {
	pool := modelAccessTestDB(t)
	seedAccess(t, pool)
	group := []string{"access/alice", "access/bea", "access/employee"}
	tests := []struct {
		name, sql string
		want      []string
	}{
		{"remove role model grant", `DELETE FROM model_assignments WHERE id='grant'`, group},
		{"restore role model grant", `INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id) VALUES('grant','access','chat','role','role')`, group},
		{"assignment expiry edit", `UPDATE model_assignments SET expires_at=now()+interval '1 hour' WHERE id='grant'`, group},
		{"disable role", `UPDATE roles SET enabled=false WHERE deployment_id='access' AND id='role'`, group},
		{"restore role", `UPDATE roles SET enabled=true WHERE deployment_id='access' AND id='role'`, group},
		{"permission change", `INSERT INTO role_permissions(deployment_id,role_id,permission_id) VALUES('access','role','models.read')`, group},
		{"disable team", `UPDATE teams SET enabled=false WHERE deployment_id='access' AND id='team'`, []string{"access/bea", "access/employee"}},
		{"restore team", `UPDATE teams SET enabled=true WHERE deployment_id='access' AND id='team'`, []string{"access/bea", "access/employee"}},
		{"remove team default", `DELETE FROM team_default_roles WHERE deployment_id='access'`, []string{"access/bea", "access/employee"}},
		{"restore team default", `INSERT INTO team_default_roles(deployment_id,team_id,role_id) VALUES('access','team','role')`, []string{"access/bea", "access/employee"}},
		{"disable model", `UPDATE models SET enabled=false WHERE deployment_id='access' AND id='chat'`, group},
		{"session revoke", `UPDATE user_sessions SET revoked_at=now() WHERE session_id='session'`, []string{"access/alice"}},
		{"agent expiry edit", `UPDATE agent_profiles SET ephemeral=true,expires_at=now()+interval '1 hour' WHERE user_id='employee'`, []string{"access/employee"}},
		{"account disable", `UPDATE users SET status='disabled' WHERE id='alice'`, []string{"access/alice"}},
		{"move direct binding", `UPDATE user_role_bindings SET user_id='bob' WHERE deployment_id='access' AND user_id='alice'`, []string{"access/alice", "access/bob"}},
		{"move team membership", `UPDATE user_team_bindings SET user_id='alice' WHERE deployment_id='access' AND user_id='bea'`, []string{"access/alice", "access/bea"}},
		{"move assignment role", `UPDATE model_assignments SET subject_id='unused' WHERE id='grant'`, []string{"access/alice", "access/bob", "access/employee"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := accessVersions(t, pool)
			accessExec(t, pool, tt.sql)
			accessChanged(t, before, accessVersions(t, pool), tt.want...)
		})
	}
}

func TestModelAccessChangeIgnoresNonPolicyWritesWithPostgres(t *testing.T) {
	pool := modelAccessTestDB(t)
	seedAccess(t, pool)
	before := accessVersions(t, pool)
	for _, sql := range []string{
		`UPDATE user_sessions SET last_seen_at=now(),client_name='renamed'`,
		`UPDATE users SET display_name='Renamed',email='test@example.invalid',updated_at=now()`,
		`UPDATE roles SET name=name||' renamed',description='renamed',updated_at=now()`,
		`UPDATE teams SET description='renamed',updated_at=now()`,
		`UPDATE models SET display_name='Renamed',updated_at=now(),enabled=enabled`,
		`UPDATE user_role_bindings SET is_primary=true`,
		`UPDATE user_team_bindings SET is_primary=true`,
		`UPDATE model_assignments SET expires_at=expires_at`,
		`UPDATE agent_profiles SET display_title='Renamed',updated_at=now()`,
		`INSERT INTO user_session_tokens(token_hash,session_id,expires_at) VALUES('test-only-old','session',now()+interval '1 hour')`,
		`UPDATE user_session_tokens SET revoked_at=now() WHERE token_hash='test-only-old'`,
		`INSERT INTO user_session_tokens(token_hash,session_id,expires_at) VALUES('test-only-new','session',now()+interval '1 hour')`,
	} {
		accessExec(t, pool, sql)
	}
	accessChanged(t, before, accessVersions(t, pool))
}

func TestModelAccessChangeDeleteAndRollbackWithPostgres(t *testing.T) {
	pool := modelAccessTestDB(t)
	seedAccess(t, pool)
	before := accessVersions(t, pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `DELETE FROM models WHERE deployment_id='access'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, accessVersions(t, pool)) {
		t.Fatal("rolled back business change left signals")
	}
	for _, tt := range []struct {
		sql  string
		want []string
	}{
		{`DELETE FROM models WHERE deployment_id='access'`, []string{"access/alice", "access/bea", "access/employee"}},
		{`DELETE FROM teams WHERE deployment_id='other'`, []string{"other/foreign"}},
		{`DELETE FROM roles WHERE deployment_id='other' AND id='role'`, []string{"other/foreign"}},
		{`DELETE FROM users WHERE id='alice'`, []string{"access/alice"}},
	} {
		before = accessVersions(t, pool)
		accessExec(t, pool, tt.sql)
		accessChanged(t, before, accessVersions(t, pool), tt.want...)
	}
	// Recreating a deleted identifier must not reset its durable version fence.
	before = accessVersions(t, pool)
	accessExec(t, pool, `INSERT INTO users(id,deployment_id,username,display_name,password_hash) VALUES('alice','access','new-alice','New','test-only-unused')`)
	accessChanged(t, before, accessVersions(t, pool), "access/alice")
	accessExec(t, pool, `INSERT INTO deployments(id,name) VALUES('cascade','Cascade')`)
	accessExec(t, pool, `INSERT INTO users(id,deployment_id,username,display_name,password_hash) VALUES('cascade-user','cascade','cascade','Test','test-only-unused')`)
	accessExec(t, pool, `DELETE FROM deployments WHERE id='cascade'`)
	for key := range accessVersions(t, pool) {
		if strings.HasPrefix(key, "cascade/") {
			t.Fatal("deployment cascade retained orphan signals")
		}
	}
}

func TestModelAccessChangeConcurrentVersionsWithPostgres(t *testing.T) {
	pool := modelAccessTestDB(t)
	seedAccess(t, pool)
	before := accessVersions(t, pool)
	const workers = 8
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			if _, err := pool.Exec(t.Context(), `SELECT queue_model_access_change('access','user','alice')`); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	after := accessVersions(t, pool)
	if after["access/alice"] != before["access/alice"]+workers {
		t.Fatal("concurrent coalescing lost a change")
	}
	// A consumer acknowledging its old read cannot swallow a newer change.
	tag, err := pool.Exec(t.Context(), `UPDATE model_access_changes SET acknowledged_version=$1 WHERE deployment_id='access' AND user_id='alice' AND version=$1`, before["access/alice"])
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatal("stale acknowledgement consumed a newer signal")
	}
	accessExec(t, pool, `UPDATE model_access_changes SET acknowledged_version=version WHERE deployment_id='access' AND user_id='alice'`)
	accessExec(t, pool, `SELECT queue_model_access_change('access','user','alice')`)
	var pending bool
	if err := pool.QueryRow(t.Context(), `SELECT version>acknowledged_version FROM model_access_changes WHERE deployment_id='access' AND user_id='alice'`).Scan(&pending); err != nil || !pending {
		t.Fatal("new change did not reopen acknowledged work")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE model_access_changes SET acknowledged_version=version+1 WHERE deployment_id='access'`); err == nil {
		t.Fatal("future acknowledgement bypassed fence")
	}
}

func TestModelAccessLicenseChangesWithPostgres(t *testing.T) {
	pool := modelAccessTestDB(t)
	seedAccess(t, pool)
	before := accessVersions(t, pool)
	accessExec(t, pool, `INSERT INTO licenses(license_id,customer_id,deployment_id,digest,key_id,issued_at,features,payload)
VALUES('test-license','test-customer','access','sha256:'||repeat('0',64),'test-key',now(),ARRAY['enterprise.models'],'{}')`)
	all := []string{"access/alice", "access/bea", "access/bob", "access/employee"}
	accessChanged(t, before, accessVersions(t, pool), all...)
	before = accessVersions(t, pool)
	accessExec(t, pool, `UPDATE licenses SET status='revoked',revoked_at=now() WHERE license_id='test-license'`)
	accessChanged(t, before, accessVersions(t, pool), all...)
	accessExec(t, pool, `INSERT INTO license_activations(id,license_id,deployment_id,user_id) VALUES('test-activation','test-license','access','alice')`)
	before = accessVersions(t, pool)
	accessExec(t, pool, `UPDATE license_activations SET last_seen_at=now()`)
	accessChanged(t, before, accessVersions(t, pool))
	accessExec(t, pool, `UPDATE license_activations SET revoked_at=now()`)
	accessChanged(t, before, accessVersions(t, pool), all...)
}

func TestModelAccessChangeUpgradeWithPostgres(t *testing.T) {
	pool := modelAccessTestDBBefore(t, func(pool *pgxpool.Pool) {
		// Build the previous release using the immutable migration sources, so
		// the real migrator must seed users that predate the new triggers.
		accessExec(t, pool, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`)
		entries, err := os.ReadDir("../db/migrations")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "052_" {
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
	})
	values := accessVersions(t, pool)
	if len(values) != 5 {
		t.Fatalf("upgrade seeded %d principals, want 5", len(values))
	}
	for _, version := range values {
		if version != 1 {
			t.Fatal("upgrade did not seed initial work")
		}
	}
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, accessVersions(t, pool)) {
		t.Fatal("repeated service startup recreated policy work")
	}
}
