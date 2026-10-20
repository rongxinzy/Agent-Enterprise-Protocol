package publicationrelay

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func accessRelayTestStore(t *testing.T) (*repository.DeploymentStore, *gorm.DB) {
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
	name := "access_relay_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	return repository.New(orm).Deployment("demo"), orm
}

func testAccessRelayWithAPI(t *testing.T, kube client.Client) {
	store, orm := accessRelayTestStore(t)
	ctx := t.Context()
	if err := orm.Exec(`INSERT INTO deployments(id,name) VALUES('demo','Relay test');
INSERT INTO roles(deployment_id,id,name) VALUES('demo','role','Role');
INSERT INTO users(id,deployment_id,username,display_name,password_hash,require_password_change,kind)
VALUES('alice','demo','alice','Alice','test-only-unused',false,'human'),('bob','demo','bob','Bob','test-only-unused',false,'human');
INSERT INTO user_role_bindings(deployment_id,user_id,role_id) VALUES('demo','alice','role'),('demo','bob','role');
INSERT INTO models(deployment_id,id,display_name,source_type,protocol,enabled,endpoint)
VALUES('demo','chat','Chat','gateway','openai-compatible',true,'https://test-only-provider.invalid');
INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id) VALUES('grant','demo','chat','role','role');
INSERT INTO user_sessions(session_id,deployment_id,user_id,topic) VALUES('alice-session','demo','alice','test'),('bob-session','demo','bob','test');
INSERT INTO user_session_tokens(token_hash,session_id,expires_at) VALUES('test-only-alice','alice-session',now()+interval '1 hour'),('test-only-bob','bob-session',now()+interval '1 hour')`).Error; err != nil {
		t.Fatal(err)
	}
	p := &Projector{Client: kube, Namespace: "aep-system"}
	a, err := NewAccessRelay(store, p, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	w := New(store, p)
	w.Access = a
	for range 2 {
		if err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	fixture := accessDelivery(t, 1, "alice", "alice-session", false)
	bob := accessDelivery(t, 1, "bob", "bob-session", false)
	before := readAccess(t, p, fixture)
	bobBefore := readAccess(t, p, bob)
	if before.Spec.Disabled || len(before.Spec.Models) != 1 || before.Spec.Models[0] != "chat" || len(before.Status.Replicas) != 0 {
		t.Fatal("DB -> producer -> outbox -> CR did not deliver role-derived allow")
	}
	if err := orm.Exec(`UPDATE users SET status='disabled' WHERE id='alice'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	denied := readAccess(t, p, fixture)
	if !denied.Spec.Disabled || len(denied.Spec.Models) != 0 || denied.Spec.Publication.Sequence <= before.Spec.Publication.Sequence ||
		readAccess(t, p, bob).ResourceVersion != bobBefore.ResourceVersion {
		t.Fatal("targeted withdrawal missing or unrelated Bob rewritten")
	}
	// Real API resourceVersion conflict: old writer pauses, newer deny commits.
	first := accessDelivery(t, 1, "race", "race-session", false)
	if err := p.ProjectAccess(ctx, first); err != nil {
		t.Fatal(err)
	}
	old := accessDelivery(t, 2, "race", "race-session", false)
	newer := accessDelivery(t, 3, "race", "race-session", true)
	paused := &Projector{Namespace: p.Namespace, Client: &faultClient{Client: kube, update: func(ctx context.Context, obj client.Object) error {
		if _, ok := obj.(*gateway.ModelAccessGrant); ok {
			if err := p.ProjectAccess(ctx, newer); err != nil {
				return err
			}
		}
		return kube.Update(ctx, obj)
	}}}
	if err := paused.ProjectAccess(ctx, old); !apierrors.IsConflict(err) {
		t.Fatalf("late update bypassed API resourceVersion: %v", err)
	}
	if err := p.ProjectAccess(ctx, old); !errors.Is(err, ErrSuperseded) {
		t.Fatal("old allow overwritten a newer tombstone")
	}
	// Status storage must not be evidence manufactured by the relay or clobbered
	// by a newer spec write. This is API isolation, not data-plane acceptance.
	live := readAccess(t, p, newer)
	live.Status.ObservedGeneration = live.Generation
	live.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse,
		Reason: "NotAdopted", Message: "No serving-process evidence", LastTransitionTime: metav1.Now()}}
	if err := kube.Status().Update(ctx, &live); err != nil {
		t.Fatal(err)
	}
	last := accessDelivery(t, 4, "race", "race-session", true)
	if err := p.ProjectAccess(ctx, last); err != nil {
		t.Fatal(err)
	}
	stored := readAccess(t, p, last)
	if len(stored.Status.Conditions) != 1 || stored.Status.Conditions[0].Reason != "NotAdopted" ||
		stored.Status.ObservedGeneration == stored.Generation {
		t.Fatal("spec delivery clobbered status or claimed current adoption")
	}
	testAccessCrashRecovery(t, store, orm, p)
	t.Log("real PostgreSQL + API: role allow, targeted deny, Bob unchanged, stale write fenced, status preserved, crash recovery")
}

func testAccessCrashRecovery(t *testing.T, store *repository.DeploymentStore, orm *gorm.DB, p *Projector) {
	t.Helper()
	item := accessDelivery(t, 1, "recovery", "recovery-session", false)
	publication, err := store.QueueModelAccessPublication(t.Context(), repository.ModelAccessIntent{
		BindingKey: item.BindingKey, SubjectID: item.SubjectID, SourceVersion: item.SourceVersion,
		IssuedAt: item.IssuedAt, ValidUntil: item.ValidUntil,
		Snapshot: repository.PublicationSnapshot{DeploymentID: item.DeploymentID, Revision: item.Revision,
			ContentHash: item.ContentHash, Payload: item.Payload}})
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.ClaimModelAccessPublication(t.Context())
	if err != nil || old == nil || old.OperationID != publication.OperationID {
		t.Fatal("missing recovery claim")
	}
	if err := p.ProjectAccess(t.Context(), *old); err != nil {
		t.Fatal(err)
	}
	before := readAccess(t, p, *old)
	// Crash after API success and before durable completion. A different worker
	// reclaims the expired lease and confirms identical CRs without a rewrite.
	if err := orm.Exec(`UPDATE model_access_publication_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE operation_id=?`, old.OperationID).Error; err != nil {
		t.Fatal(err)
	}
	restarted, err := NewAccessRelay(store, p, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if err := restarted.deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	confirmed, err := store.LatestModelAccessPublication(t.Context(), item.BindingKey)
	if err != nil || confirmed.Phase != "projected" || confirmed.OperationID != old.OperationID ||
		readAccess(t, p, *old).ResourceVersion != before.ResourceVersion {
		t.Fatal("restart failed to confirm immutable delivery idempotently")
	}
	if err := store.CheckModelAccessDelivery(t.Context(), *old); !errors.Is(err, repository.ErrPublicationClaimLost) {
		t.Fatal("old process retained a usable lease")
	}
}
