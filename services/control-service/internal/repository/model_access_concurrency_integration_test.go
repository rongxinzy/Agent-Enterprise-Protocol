package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func waitForAccessLock(t *testing.T, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND wait_event='advisory')`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("concurrent dependency mutation did not wait for configuration lock")
		case <-ticker.C:
		}
	}
}

func TestModelAccessConcurrentMembershipAndPolicyWithPostgres(t *testing.T) {
	for _, order := range []string{"membership-first", "policy-first"} {
		t.Run(order, func(t *testing.T) {
			pool := modelAccessTestDB(t)
			seedAccess(t, pool)
			before := accessVersions(t, pool)
			firstSQL := `INSERT INTO user_role_bindings(deployment_id,user_id,role_id) VALUES('access','bob','role')`
			secondSQL := `UPDATE roles SET enabled=false WHERE deployment_id='access' AND id='role'`
			if order == "policy-first" {
				firstSQL, secondSQL = secondSQL, firstSQL
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			first, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = first.Rollback(context.Background()) }()
			if _, err := first.Exec(ctx, firstSQL); err != nil {
				t.Fatal(err)
			}
			connection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Release()
			pid := connection.Conn().PgConn().PID()
			done := make(chan error, 1)
			go func() {
				_, err := connection.Exec(ctx, secondSQL)
				done <- err
			}()
			// A membership must not publish old policy while its concurrent policy
			// mutation is uncommitted. Conversely fanout must see a membership
			// committed while its original UPDATE statement was waiting.
			waitForAccessLock(t, pool, pid)
			if err := first.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			after := accessVersions(t, pool)
			minimum := before["access/bob"] + 1
			if order == "membership-first" {
				minimum++ // Separate signal from the later policy fanout.
			}
			if after["access/bob"] < minimum {
				t.Fatal("policy fanout missed the newly committed member")
			}
			accessChanged(t, before, after, "access/alice", "access/bea", "access/employee", "access/bob")
		})
	}
}
