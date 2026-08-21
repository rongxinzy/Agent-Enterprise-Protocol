package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	const lockID int64 = 0x4145505F4D494752
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := connection.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			_ = connection.Conn().Close(unlockContext)
		}
	}()
	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := connection.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, entry.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		content, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := connection.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(content)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
