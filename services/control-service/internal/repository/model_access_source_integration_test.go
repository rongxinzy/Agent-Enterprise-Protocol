package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func sourceCaptureTestStore(t *testing.T) (*DeploymentStore, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("AEP_REFERENCE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AEP_REFERENCE_TEST_DATABASE_URL to a disposable PostgreSQL server")
	}
	admin, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "access_source_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+name); err != nil {
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
	return New(orm).Deployment("source"), pool
}

func sourceExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func seedSource(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	sourceExec(t, pool, `INSERT INTO deployments(id,name) VALUES('source','Test'),('foreign','Other')`)
	sourceExec(t, pool, `INSERT INTO roles(deployment_id,id,name,enabled) VALUES('source','direct','Direct',true),('source','inherited','Inherited',true),('source','off','Off',false),('foreign','direct','Foreign',true)`)
	sourceExec(t, pool, `INSERT INTO teams(deployment_id,id,name,enabled) VALUES('source','team','Team',true),('source','off','Off',false)`)
	sourceExec(t, pool, `INSERT INTO users(id,deployment_id,username,display_name,password_hash,require_password_change,kind) VALUES('alice','source','alice','Alice','test-only-unused',false,'human'),('employee','source','employee','Employee','test-only-unused',false,'agent'),('foreign-user','foreign','foreign','Foreign','test-only-unused',false,'human')`)
	sourceExec(t, pool, `INSERT INTO user_role_bindings(deployment_id,user_id,role_id) VALUES('source','alice','direct'),('source','alice','off'),('foreign','foreign-user','direct')`)
	sourceExec(t, pool, `INSERT INTO user_team_bindings(deployment_id,user_id,team_id) VALUES('source','alice','team'),('source','alice','off'),('source','employee','team')`)
	sourceExec(t, pool, `INSERT INTO team_default_roles(deployment_id,team_id,role_id) VALUES('source','team','inherited'),('source','team','direct'),('source','off','off')`)
	for _, id := range []string{"chat", "finite", "off-role", "expired", "disabled", "legacy-only"} {
		sourceExec(t, pool, `INSERT INTO models(deployment_id,id,display_name,source_type,protocol,enabled,endpoint) VALUES('source',$1,$1,'gateway','openai-compatible',$1<>'disabled','http://test-only-provider.invalid')`, id)
	}
	sourceExec(t, pool, `INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id,expires_at) VALUES
('direct-chat','source','chat','role','direct',NULL),
('inherited-chat','source','chat','role','inherited',now()+interval '2 minutes'),
('finite-short','source','finite','role','direct',now()+interval '3 minutes'),
('finite-long','source','finite','role','inherited',now()+interval '7 minutes'),
('disabled-model','source','disabled','role','direct',NULL),
('disabled-role','source','off-role','role','off',NULL),
('expired-grant','source','expired','role','direct',now()-interval '1 minute')`)
	sourceExec(t, pool, `INSERT INTO agent_profiles(deployment_id,user_id,home_team_id,ephemeral,expires_at) VALUES('source','employee','team',true,now()+interval '10 minutes')`)
	sourceExec(t, pool, `INSERT INTO user_sessions(session_id,deployment_id,user_id,topic,revoked_at) VALUES('a-live','source','alice','test',NULL),('b-expired','source','alice','test',NULL),('c-revoked','source','alice','test',now()),('d-no-token','source','alice','test',NULL),('foreign-session','foreign','foreign-user','test',NULL)`)
	sourceExec(t, pool, `INSERT INTO user_session_tokens(token_hash,session_id,expires_at,revoked_at) VALUES
('test-only-live-old','a-live',now()+interval '20 minutes',NULL),
('test-only-live-new','a-live',now()+interval '30 minutes',NULL),
('test-only-revoked-token','a-live',now()+interval '2 hours',now()),
('test-only-expired-token','b-expired',now()-interval '1 minute',NULL),
('test-only-revoked-session','c-revoked',now()+interval '30 minutes',NULL),
('test-only-foreign-token','foreign-session',now()+interval '30 minutes',NULL)`)
	sourceExec(t, pool, `INSERT INTO licenses(license_id,customer_id,deployment_id,digest,key_id,status,issued_at,expires_at,grace_ends_at,features,payload) VALUES
('perpetual','test','source','sha256:'||repeat('a',64),'test','active',now(),NULL,NULL,'{model,model,chat}','{"privateMetadata":"test-only-omit"}'),
('grace','test','source','sha256:'||repeat('b',64),'test','active',now(),now()-interval '1 minute',now()+interval '9 minutes','{}','{}'),
('expired','test','source','sha256:'||repeat('c',64),'test','active',now(),now()-interval '2 minutes',now()-interval '1 minute','{}','{}'),
('revoked','test','source','sha256:'||repeat('d',64),'test','revoked',now(),NULL,NULL,'{}','{}'),
('foreign-license','test','foreign','sha256:'||repeat('e',64),'test','active',now(),NULL,NULL,'{}','{}')`)
}

func readSource(t *testing.T, store *DeploymentStore, user string) ModelAccessSource {
	t.Helper()
	var result ModelAccessSource
	if err := store.WithModelAccessSource(t.Context(), user, func(_ *DeploymentStore, source ModelAccessSource) error {
		result = source
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestModelAccessSourceRoleUnionAndBindingWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	source := readSource(t, store, "alice")
	if !source.Exists || source.Disabled || source.Kind != "human" || source.ProfileMissing || source.LegacyDirectGrants {
		t.Fatalf("unexpected subject state: %+v", source)
	}
	if !slices.Equal(source.RoleIDs, []string{"direct", "inherited"}) || !slices.Equal(source.TeamIDs, []string{"team"}) {
		t.Fatal("effective role union duplicated a direct/inherited role or included disabled membership")
	}
	if len(source.Models) != 2 || source.Models[0].ID != "chat" || source.Models[0].ExpiresAt != nil || source.Models[1].ID != "finite" {
		t.Fatal("models are not the enabled, unexpired effective-role union")
	}
	deadline := source.Models[1].ExpiresAt
	if deadline == nil || deadline.Sub(source.ObservedAt) < 6*time.Minute || deadline.Sub(source.ObservedAt) > 8*time.Minute {
		t.Fatal("finite grant union used the first expiry instead of the last effective grant")
	}
	if len(source.Sessions) != 1 || source.Sessions[0].ID != "a-live" || source.Sessions[0].ExpiresAt.Sub(source.ObservedAt) < 29*time.Minute {
		t.Fatal("session binding did not retain only the last live refresh deadline")
	}
	if len(source.Licenses) != 2 || source.Licenses[0].ID != "grace" || source.Licenses[0].ExpiresAt == nil || source.Licenses[1].ID != "perpetual" || source.Licenses[1].ExpiresAt != nil {
		t.Fatal("license ID/digest/grace binding was lost or crossed deployment boundaries")
	}
	if source.Licenses[1].Digest != "sha256:"+strings.Repeat("a", 64) || !slices.Equal(source.Licenses[1].Features, []string{"chat", "model"}) {
		t.Fatal("exact license digest or canonical features were lost")
	}
	if next := source.NextExpiry(); next == nil || !next.Equal(*deadline) {
		t.Fatal("natural expiry scheduler would miss the effective model deadline")
	}
	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"test-only-", "password", "token_hash", "privateMetadata", "foreign-"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("source exposed excluded field/value %q", forbidden)
		}
	}
	before := source
	sourceExec(t, pool, `UPDATE user_session_tokens SET revoked_at=now() WHERE token_hash='test-only-live-old'`)
	sourceExec(t, pool, `UPDATE users SET display_name='Renamed' WHERE id='alice'`)
	sourceExec(t, pool, `UPDATE model_assignments SET expires_at=now()+interval '1 minute' WHERE id='inherited-chat'`)
	after := readSource(t, store, "alice")
	after.ObservedAt = before.ObservedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("token rotation, display metadata or redundant grant expiry changed effective policy source")
	}
}

func TestModelAccessSourceChangesAndLegacyBoundaryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	sourceExec(t, pool, `INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id) VALUES('legacy-user','source','legacy-only','user','alice'),('legacy-team','source','legacy-only','team','team')`)
	source := readSource(t, store, "alice")
	if !source.LegacyDirectGrants || len(source.Models) != 2 {
		t.Fatal("legacy assignments were silently projected as role grants or were not flagged")
	}
	sourceExec(t, pool, `UPDATE teams SET enabled=false WHERE deployment_id='source' AND id='team'`)
	source = readSource(t, store, "alice")
	if !slices.Equal(source.RoleIDs, []string{"direct"}) || len(source.TeamIDs) != 0 || !source.LegacyDirectGrants {
		t.Fatal("disabled team retained inherited authorization or hid a direct legacy grant")
	}
	sourceExec(t, pool, `DELETE FROM model_assignments WHERE id='legacy-user'`)
	if source = readSource(t, store, "alice"); source.LegacyDirectGrants {
		t.Fatal("disabled team produced an effective legacy grant")
	}
	for _, sql := range []string{
		`UPDATE users SET require_password_change=true WHERE id='alice'`,
		`UPDATE users SET require_password_change=false,status='disabled' WHERE id='alice'`,
	} {
		sourceExec(t, pool, sql)
		if !readSource(t, store, "alice").Disabled {
			t.Fatal("disabled/password-change account was treated as active")
		}
	}
	sourceExec(t, pool, `DELETE FROM users WHERE id='alice'`)
	if source = readSource(t, store, "alice"); source.Exists || !source.Disabled || len(source.Models) != 0 || len(source.Sessions) != 0 {
		t.Fatal("deleted subject cannot produce an unambiguous deny/tombstone source")
	}
	if source = readSource(t, store, "foreign-user"); source.Exists || !source.Disabled || len(source.Sessions) != 0 {
		t.Fatal("caller-controlled subject crossed deployment boundary")
	}
}

func TestModelAccessSourceEmployeeExpiryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	source := readSource(t, store, "employee")
	if source.Kind != "agent" || source.Disabled || source.SubjectExpiresAt == nil || source.ProfileMissing || !slices.Equal(source.RoleIDs, []string{"direct", "inherited"}) {
		t.Fatal("employee did not derive the same role-based grants as a human")
	}
	sourceExec(t, pool, `UPDATE agent_profiles SET expires_at=now()-interval '1 second' WHERE user_id='employee'`)
	if !readSource(t, store, "employee").Disabled {
		t.Fatal("expired ephemeral employee was not disabled")
	}
	sourceExec(t, pool, `DELETE FROM agent_profiles WHERE user_id='employee'`)
	if source = readSource(t, store, "employee"); !source.ProfileMissing || !source.Disabled {
		t.Fatal("missing employee profile was silently treated as a trusted workload")
	}
}

func TestModelAccessSourceCaptureRollbackWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	sourceExec(t, pool, `CREATE TABLE capture_receipts(id text PRIMARY KEY)`)
	abort := errors.New("test-only capture aborted")
	for _, fail := range []bool{true, false} {
		err := store.WithModelAccessSource(t.Context(), "alice", func(tx *DeploymentStore, source ModelAccessSource) error {
			if err := tx.db.Exec(`INSERT INTO capture_receipts(id) VALUES(?)`, source.SubjectID).Error; err != nil {
				return err
			}
			if fail {
				return abort
			}
			return nil
		})
		if fail && !errors.Is(err, abort) || !fail && err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM capture_receipts`).Scan(&count); err != nil || fail && count != 0 || !fail && count != 1 {
			t.Fatal("capture did not commit/roll back with its source transaction")
		}
	}
	if err := store.WithModelAccessSource(t.Context(), "alice", nil); err == nil {
		t.Fatal("missing capture accepted")
	}
	if err := store.WithModelAccessSource(t.Context(), "", func(*DeploymentStore, ModelAccessSource) error { return nil }); err == nil {
		t.Fatal("missing subject accepted")
	}
	if err := store.WithTransaction(t.Context(), func(tx *DeploymentStore) error {
		if err := tx.db.Exec(`SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`).Error; err != nil {
			return err
		}
		return tx.WithModelAccessSource(t.Context(), "alice", func(*DeploymentStore, ModelAccessSource) error {
			t.Error("unsafe inherited snapshot reached publication capture")
			return nil
		})
	}); err == nil || !strings.Contains(err.Error(), "read committed") {
		t.Fatal("inherited old MVCC snapshot was not refused")
	}
}

func TestModelAccessSourceNextExpiry(t *testing.T) {
	now := time.Now().UTC()
	passed, soon, later := now.Add(-time.Second), now.Add(time.Minute), now.Add(time.Hour)
	source := ModelAccessSource{ObservedAt: now, SubjectExpiresAt: &later,
		Models: []ModelAccessModel{{ExpiresAt: &passed}}, Sessions: []ModelAccessSession{{ExpiresAt: later}},
		Licenses: []ModelAccessLicense{{ExpiresAt: &soon}, {}}}
	if got := source.NextExpiry(); got == nil || !got.Equal(soon) {
		t.Fatal("license grace deadline missing from expiry scheduling")
	}
	source.SubjectExpiresAt = &passed
	source.Sessions = nil
	source.Licenses = nil
	if source.NextExpiry() != nil {
		t.Fatal("past/unbounded source caused an immediate reschedule loop")
	}
}
