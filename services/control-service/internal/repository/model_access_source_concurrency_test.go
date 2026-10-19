package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func waitForSourceReader(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory'
AND query LIKE '%pg_advisory_xact_lock%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("source read did not wait for configuration transaction")
		case <-ticker.C:
		}
	}
}

func TestModelAccessSourceSeesCommitAfterLockWaitWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('source',101))`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		source ModelAccessSource
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var value ModelAccessSource
		err := store.WithModelAccessSource(ctx, "alice", func(_ *DeploymentStore, source ModelAccessSource) error {
			value = source
			return nil
		})
		done <- result{value, err}
	}()
	waitForSourceReader(t, pool, ctx)
	// This deadline is AFTER the blocked reader transaction began. Using now()
	// would incorrectly keep these grants alive after the configuration commit.
	var expiredAt time.Time
	if err := writer.QueryRow(ctx, `UPDATE model_assignments SET expires_at=clock_timestamp()
WHERE id='direct-chat' RETURNING expires_at`).Scan(&expiredAt); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `UPDATE model_assignments SET expires_at=$1 WHERE deployment_id='source'`, expiredAt); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.source.Models) != 0 || got.source.ObservedAt.Before(expiredAt) {
			t.Fatal("source used pre-wait policy/time after the mutation committed")
		}
	case <-ctx.Done():
		t.Fatal("source failed to resume after configuration commit")
	}
}

func TestModelAccessSourceLockCoversCaptureWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// A lock_timeout is evidence from PostgreSQL, not an arbitrary sleep or an
	// assumption about which goroutine runs first. The callback must still own
	// the shared configuration lock while capturing/acknowledging its source.
	if err := store.WithModelAccessSource(ctx, "alice", func(_ *DeploymentStore, _ ModelAccessSource) error {
		writer, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = writer.Rollback(context.Background()) }()
		if _, err := writer.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
			return err
		}
		if _, err := writer.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('source',101))`); err == nil {
			t.Error("capture callback released the configuration lock before commit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// After commit the lock must be released, including for another writer.
	if _, err := pool.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('source',101))`); err != nil {
		t.Fatal(err)
	}
}
