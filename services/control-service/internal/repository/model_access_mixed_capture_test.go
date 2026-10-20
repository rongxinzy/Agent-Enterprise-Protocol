package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
)

func captureBindings(t *testing.T, store *DeploymentStore, user string) *ModelAccessCapture {
	t.Helper()
	value, err := store.CaptureModelAccessBindings(t.Context(), user, 5*time.Minute)
	if err != nil || value == nil {
		t.Fatal("expected complete binding capture", err)
	}
	return value
}

func workloadPolicy(t *testing.T, capture *ModelAccessCapture) (ModelAccessPublication, gatewayaccess.Snapshot) {
	t.Helper()
	for _, publication := range capture.Publications {
		policy := capturedPolicy(t, publication)
		if policy.Binding.Kind == "kubernetes" && !policy.Retired {
			return publication, policy
		}
	}
	t.Fatal("current workload missing from capture")
	return ModelAccessPublication{}, gatewayaccess.Snapshot{}
}

func TestMixedCaptureAllBindingsAndObservationRecoveryWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	sourceExec(t, pool, `INSERT INTO user_sessions(session_id,deployment_id,user_id,topic) VALUES('employee-session','source','employee','fixture')`)
	sourceExec(t, pool, `INSERT INTO user_session_tokens(token_hash,session_id,expires_at) VALUES('fixture-employee-token','employee-session',now()+interval '1 hour')`)
	w := workloadObservation()
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := captureBindings(t, store, "employee")
	p, policy := workloadPolicy(t, first)
	if len(first.Publications) != 2 || policy.Disabled || policy.Subject.Kind != "agent" || policy.Subject.ID != "employee" ||
		p.BindingKey != key || len(policy.Models) != 2 || len(policy.Licenses) != 2 || time.Until(p.ValidUntil) > time.Minute || first.NextCaptureAt == nil {
		t.Fatal("mixed capture lost workload identity, role-derived resources or natural verification bound")
	}
	if v, a := captureAcknowledgement(t, pool, "employee"); v != a || v != first.SourceVersion {
		t.Fatal("complete binding set did not atomically acknowledge its source")
	}
	// Trusted observation expiry is recoverable, unlike confirmed retirement.
	sourceExec(t, pool, `UPDATE model_access_workloads SET verified_at=clock_timestamp()-interval '2 minutes',verified_until=clock_timestamp()-interval '1 minute' WHERE user_id='employee'`)
	expired := captureBindings(t, store, "employee")
	_, paused := workloadPolicy(t, expired)
	if !paused.Disabled || paused.Retired || len(paused.Models) != 0 || len(paused.Licenses) != 0 {
		t.Fatal("expired observation retained allow or permanently destroyed a recoverable identity")
	}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, time.Minute); err != nil {
		t.Fatal(err)
	}
	recovered := captureBindings(t, store, "employee")
	_, active := workloadPolicy(t, recovered)
	if active.Disabled || active.BindingKey() != key || len(active.Models) != 2 {
		t.Fatal("new live evidence could not recover the same immutable binding")
	}
	if err := store.RetireModelAccessWorkload(t.Context(), "employee", key); err != nil {
		t.Fatal(err)
	}
	retired := captureBindings(t, store, "employee")
	for _, publication := range retired.Publications {
		policy := capturedPolicy(t, publication)
		if publication.BindingKey == key && (!policy.Retired || !policy.Disabled) {
			t.Fatal("confirmed retirement lost its terminal tombstone")
		}
	}
}

func TestMixedCaptureRollbackAndUnrelatedSubjectWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	alice := captureBindings(t, store, "alice")
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", workloadObservation(), time.Minute); err != nil {
		t.Fatal(err)
	}
	before, ack := captureAcknowledgement(t, pool, "employee")
	sourceExec(t, pool, `CREATE FUNCTION reject_mixed_ack() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.acknowledged_version>OLD.acknowledged_version THEN RAISE EXCEPTION 'fixture ack rejection'; END IF; RETURN NEW; END; $$;
CREATE TRIGGER reject_mixed_ack BEFORE UPDATE ON model_access_changes FOR EACH ROW EXECUTE FUNCTION reject_mixed_ack()`)
	if _, err := store.CaptureModelAccessBindings(t.Context(), "employee", time.Minute); err == nil {
		t.Fatal("injected acknowledgement failure did not abort mixed capture")
	}
	if v, a := captureAcknowledgement(t, pool, "employee"); v != before || a != ack {
		t.Fatal("failed capture advanced source acknowledgement")
	}
	var rows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM model_access_publications WHERE subject_id='employee'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("failed capture leaked partial publication/outbox intent", rows, err)
	}
	sourceExec(t, pool, `DROP TRIGGER reject_mixed_ack ON model_access_changes`)
	captureBindings(t, store, "employee")
	latest, err := store.LatestModelAccessPublication(t.Context(), alice.Publications[0].BindingKey)
	if err != nil || latest.OperationID != alice.Publications[0].OperationID {
		t.Fatal("employee work republished unrelated human policy", err)
	}
	if _, err := store.CaptureModelAccessSessions(t.Context(), "employee", time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("legacy session-only API no longer refuses incomplete coverage")
	}
}
