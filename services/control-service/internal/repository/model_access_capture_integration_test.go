package repository

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func captureSessions(t *testing.T, store *DeploymentStore, user string) *ModelAccessCapture {
	t.Helper()
	value, err := store.CaptureModelAccessSessions(t.Context(), user, 5*time.Minute)
	if err != nil || value == nil {
		t.Fatalf("expected captured session work: %v", err)
	}
	return value
}

func capturedPolicy(t *testing.T, publication ModelAccessPublication) gatewayaccess.Snapshot {
	t.Helper()
	value, err := gatewayaccess.Decode(publication.Payload, publication.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func captureAcknowledgement(t *testing.T, pool *pgxpool.Pool, user string) (int64, int64) {
	t.Helper()
	var version, acknowledged int64
	if err := pool.QueryRow(t.Context(), `SELECT version,acknowledged_version FROM model_access_changes
WHERE deployment_id='source' AND user_id=$1`, user).Scan(&version, &acknowledged); err != nil {
		t.Fatal(err)
	}
	return version, acknowledged
}

func TestAccessCaptureAllSessionsAndTargetedWithdrawalWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	sourceExec(t, pool, `INSERT INTO user_sessions(session_id,deployment_id,user_id,topic) VALUES('z-live','source','alice','test'),('employee-session','source','employee','test')`)
	sourceExec(t, pool, `INSERT INTO user_session_tokens(token_hash,session_id,expires_at) VALUES('test-only-z','z-live',now()+interval '1 hour'),('test-only-employee','employee-session',now()+interval '1 hour')`)
	alice := captureSessions(t, store, "alice")
	employee := captureSessions(t, store, "employee")
	if len(alice.Publications) != 2 || len(employee.Publications) != 1 || alice.NextCaptureAt == nil {
		t.Fatal("capture omitted a live binding or its durable refresh")
	}
	for _, publication := range alice.Publications {
		policy := capturedPolicy(t, publication)
		if policy.Disabled || policy.Retired || !slices.Equal(policy.Models, []string{"chat", "finite"}) || len(policy.Licenses) != 2 || publication.Sequence != 1 {
			t.Fatal("session projection lost effective roles/models/licenses")
		}
	}
	if version, ack := captureAcknowledgement(t, pool, "alice"); version != ack || version != alice.SourceVersion {
		t.Fatal("captured source version was not acknowledged")
	}
	if value, err := store.CaptureModelAccessSessions(t.Context(), "alice", 5*time.Minute); err != nil || value != nil {
		t.Fatal("already acknowledged source was recaptured without due work")
	}
	sourceExec(t, pool, `UPDATE users SET status='disabled' WHERE id='alice'`)
	withdrawn := captureSessions(t, store, "alice")
	if len(withdrawn.Publications) != 2 {
		t.Fatal("account withdrawal omitted a binding")
	}
	for _, publication := range withdrawn.Publications {
		policy := capturedPolicy(t, publication)
		if !policy.Disabled || len(policy.Models) != 0 || publication.Sequence != 2 {
			t.Fatal("disabled account retained a session allow")
		}
	}
	unchanged, err := store.LatestModelAccessPublication(t.Context(), employee.Publications[0].BindingKey)
	if err != nil || unchanged.OperationID != employee.Publications[0].OperationID {
		t.Fatal("another subject was republished after targeted withdrawal")
	}
}

func TestAccessCaptureNoopAndRollbackWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	first := captureSessions(t, store, "alice")
	sourceExec(t, pool, `INSERT INTO role_permissions(deployment_id,role_id,permission_id) VALUES('source','direct','models.read')`)
	second := captureSessions(t, store, "alice")
	if second.SourceVersion <= first.SourceVersion || second.Publications[0].OperationID != first.Publications[0].OperationID || !second.NextCaptureAt.Equal(*first.NextCaptureAt) {
		t.Fatal("semantically unchanged work recreated a publication or refresh deadline")
	}
	sourceExec(t, pool, `UPDATE users SET status='disabled' WHERE id='alice'`)
	version, ack := captureAcknowledgement(t, pool, "alice")
	sourceExec(t, pool, `CREATE FUNCTION abort_access_ack() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.acknowledged_version>OLD.acknowledged_version THEN RAISE EXCEPTION 'test-only ack failure'; END IF; RETURN NEW; END; $$;
CREATE TRIGGER abort_access_ack BEFORE UPDATE ON model_access_changes FOR EACH ROW EXECUTE FUNCTION abort_access_ack()`)
	if _, err := store.CaptureModelAccessSessions(t.Context(), "alice", 5*time.Minute); err == nil {
		t.Fatal("injected acknowledgement failure did not abort capture")
	}
	if v, a := captureAcknowledgement(t, pool, "alice"); v != version || a != ack {
		t.Fatal("failed capture acknowledged work")
	}
	latest, err := store.LatestModelAccessPublication(t.Context(), first.Publications[0].BindingKey)
	if err != nil || latest.OperationID != first.Publications[0].OperationID {
		t.Fatal("failed acknowledgement left a partial new publication")
	}
	var outboxRows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM model_access_publication_outbox`).Scan(&outboxRows); err != nil || outboxRows != 1 {
		t.Fatal("failed capture left an orphan delivery")
	}
	sourceExec(t, pool, `DROP TRIGGER abort_access_ack ON model_access_changes`)
	captureSessions(t, store, "alice")
}

func TestAccessCaptureRetiresDeletedBindingsWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	first := captureSessions(t, store, "alice")
	sourceExec(t, pool, `DELETE FROM user_sessions WHERE session_id='a-live'`)
	retired := captureSessions(t, store, "alice")
	policy := capturedPolicy(t, retired.Publications[0])
	if !policy.Disabled || !policy.Retired || len(policy.Models) != 0 || retired.NextCaptureAt != nil || retired.Publications[0].BindingKey != first.Publications[0].BindingKey {
		t.Fatal("deleted session did not retain an immutable terminal binding tombstone")
	}
	sourceExec(t, pool, `INSERT INTO user_sessions(session_id,deployment_id,user_id,topic) VALUES('a-live','source','alice','test')`)
	sourceExec(t, pool, `INSERT INTO user_session_tokens(token_hash,session_id,expires_at) VALUES('test-only-reused-session','a-live',now()+interval '1 hour')`)
	if _, err := store.CaptureModelAccessSessions(t.Context(), "alice", 5*time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("deleted session ID reactivated an old signed binding")
	}
	if v, a := captureAcknowledgement(t, pool, "alice"); v <= a {
		t.Fatal("unsafe reused binding was acknowledged")
	}
	sourceExec(t, pool, `DELETE FROM users WHERE id='alice'`)
	deleted := captureSessions(t, store, "alice")
	if p := capturedPolicy(t, deleted.Publications[0]); !p.Retired || !p.Disabled {
		t.Fatal("deleted account lost its deny history")
	}
}

func TestAccessCaptureLegacyAndWorkloadBoundariesWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	captureSessions(t, store, "alice")
	sourceExec(t, pool, `INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id) VALUES('legacy','source','legacy-only','user','alice')`)
	if _, err := store.CaptureModelAccessSessions(t.Context(), "alice", 5*time.Minute); !errors.Is(err, ErrAccessLegacyGrants) {
		t.Fatal("legacy direct grants were silently lost at cutover")
	}
	if v, a := captureAcknowledgement(t, pool, "alice"); v <= a {
		t.Fatal("legacy compatibility failure was acknowledged")
	}
	// Compatibility errors must not prevent a disabled account's deny capture.
	sourceExec(t, pool, `UPDATE users SET status='disabled' WHERE id='alice'`)
	if p := capturedPolicy(t, captureSessions(t, store, "alice").Publications[0]); !p.Disabled {
		t.Fatal("legacy compatibility check blocked account withdrawal")
	}
	policy := gatewayaccess.Snapshot{Schema: "aep-model-access/v1", DeploymentID: "source",
		Subject: gateway.ModelAccessSubject{Kind: "agent", ID: "employee"},
		Binding: gateway.ModelAccessBinding{Kind: "kubernetes", Workload: &gateway.ModelAccessWorkload{
			Namespace: "employees", ServiceAccountName: "employee", ServiceAccountUID: "sa-uid", EmployeeUID: "employee-uid", Audience: "model-gateway"}}, Models: []string{"chat"}}
	payload, hash, err := policy.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	version, _ := captureAcknowledgement(t, pool, "employee")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := store.QueueModelAccessPublication(t.Context(), ModelAccessIntent{BindingKey: policy.BindingKey(), SubjectID: "employee", SourceVersion: version,
		Snapshot: PublicationSnapshot{Revision: "test-workload", ContentHash: hash, Payload: payload}, IssuedAt: now, ValidUntil: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureModelAccessSessions(t.Context(), "employee", 5*time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("session-only capture silently acknowledged unimplemented workload bindings")
	}
}

func TestAccessCaptureConcurrentAndBoundedDiscoveryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	var wg sync.WaitGroup
	results := make(chan *ModelAccessCapture, 8)
	errors := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			value, err := store.CaptureModelAccessSessions(t.Context(), "alice", 5*time.Minute)
			results <- value
			errors <- err
		})
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	captures := 0
	for value := range results {
		if value != nil {
			captures++
		}
	}
	if captures != 1 {
		t.Fatal("parallel readers captured the same source repeatedly")
	}
	ids, err := store.PendingModelAccessSubjects(t.Context(), "", 1)
	if err != nil || !slices.Equal(ids, []string{"employee"}) {
		t.Fatal("bounded discovery returned acknowledged or foreign deployment work")
	}
	ids, err = store.PendingModelAccessSubjects(t.Context(), "employee", 1)
	if err != nil || len(ids) != 0 {
		t.Fatal("discovery cursor did not advance")
	}
	if _, err := store.PendingModelAccessSubjects(t.Context(), "", 0); err == nil {
		t.Fatal("unbounded discovery accepted")
	}
}
