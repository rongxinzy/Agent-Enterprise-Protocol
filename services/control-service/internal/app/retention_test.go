package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestCleanupRetentionDeletesBoundedExpiredData(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	application.Config.RetentionCleanupBatchSize = 100
	application.Config.AuditRetention = 365 * 24 * time.Hour
	application.Config.TelemetryRetention = 90 * 24 * time.Hour
	application.Config.OperationalRetention = 30 * 24 * time.Hour
	application.Config.LoginFailureWindow = 15 * time.Minute
	application.Config.LoginBackoffMax = time.Hour
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).WithArgs(retentionAdvisoryLockID).
		WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	expectRetentionDelete(pool, `DELETE FROM authentication_audit_events`, now.Add(-application.Config.AuditRetention), 100, 1)
	expectRetentionDelete(pool, `DELETE FROM credential_resolution_audit`, now.Add(-application.Config.AuditRetention), 100, 2)
	expectRetentionDelete(pool, `DELETE FROM license_audit_events`, now.Add(-application.Config.AuditRetention), 100, 3)
	expectRetentionDelete(pool, `DELETE FROM telemetry_events`, now.Add(-application.Config.TelemetryRetention), 100, 4)
	expectRetentionDelete(pool, `DELETE FROM skill_sync_results`, now.Add(-application.Config.TelemetryRetention), 100, 5)
	expectRetentionDelete(pool, `DELETE FROM session_control_deliveries`, now.Add(-application.Config.OperationalRetention), 100, 6)
	pool.ExpectExec(`DELETE FROM control_events`).WithArgs(now.Add(-application.Config.OperationalRetention), 100, now).
		WillReturnResult(pgxmock.NewResult("DELETE", 7))
	expectRetentionDelete(pool, `DELETE FROM user_session_tokens`, now.Add(-application.Config.OperationalRetention), 100, 8)
	pool.ExpectExec(`DELETE FROM user_sessions`).WithArgs(now.Add(-application.Config.OperationalRetention), 100, now).
		WillReturnResult(pgxmock.NewResult("DELETE", 9))
	expectRetentionDelete(pool, `DELETE FROM login_rate_limits`, now.Add(-time.Hour), 100, 10)
	expectRetentionDelete(pool, `DELETE FROM skill_assignments`, now, 100, 0)
	expectRetentionDelete(pool, `DELETE FROM model_assignments`, now, 100, 0)
	expectRetentionDelete(pool, `DELETE FROM credential_assignments`, now, 100, 0)
	expectRetentionDelete(pool, `DELETE FROM data_scope_rules`, now, 100, 0)
	pool.ExpectCommit()

	result, err := application.CleanupRetention(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.LockAcquired || result.Total() != 55 || result.Sessions != 9 || result.LoginRateLimits != 10 {
		t.Fatalf("unexpected retention result: %#v", result)
	}
}

func TestCleanupRetentionSkipsWhenAnotherReplicaHoldsLock(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	application.Config.RetentionCleanupBatchSize = 100
	application.Config.OperationalRetention = 24 * time.Hour
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).WithArgs(retentionAdvisoryLockID).
		WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(false))
	pool.ExpectRollback()

	result, err := application.CleanupRetention(context.Background(), time.Now())
	if err != nil || result.LockAcquired || result.Total() != 0 {
		t.Fatalf("CleanupRetention() = %#v, %v", result, err)
	}
}

func TestCleanupRetentionRollsBackOnDeleteFailure(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	application.Config.RetentionCleanupBatchSize = 100
	application.Config.AuditRetention = 24 * time.Hour
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).WithArgs(retentionAdvisoryLockID).
		WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	pool.ExpectExec(`DELETE FROM authentication_audit_events`).WithArgs(pgxmock.AnyArg(), 100).
		WillReturnError(errors.New("database unavailable"))
	pool.ExpectRollback()

	_, err := application.CleanupRetention(context.Background(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "delete expired authentication audit") {
		t.Fatalf("CleanupRetention() error = %v", err)
	}
}

func TestCleanupRetentionRejectsUnavailableDatabaseAndInvalidBatch(t *testing.T) {
	if _, err := (&App{}).CleanupRetention(context.Background(), time.Now()); err == nil {
		t.Fatal("CleanupRetention accepted an unavailable database")
	}
	application, _, _ := newMockApplication(t)
	if _, err := application.CleanupRetention(context.Background(), time.Now()); err == nil {
		t.Fatal("CleanupRetention accepted a zero batch size")
	}
}

func TestRunRetentionCanBeDisabled(t *testing.T) {
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		(&App{}).RunRetention(context.Background())
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("disabled retention worker did not return")
	}
}

func expectRetentionDelete(pool pgxmock.PgxPoolIface, expression string, cutoff time.Time, batchSize int, rows int64) {
	pool.ExpectExec(expression).WithArgs(cutoff, batchSize).
		WillReturnResult(pgxmock.NewResult("DELETE", rows))
}
