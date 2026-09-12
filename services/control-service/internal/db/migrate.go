package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type migrationConnection interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
	Close(context.Context) error
}

type pooledMigrationConnection struct {
	connection *pgxpool.Conn
}

func (c pooledMigrationConnection) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return c.connection.Exec(ctx, sql, arguments...)
}

func (c pooledMigrationConnection) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	return c.connection.QueryRow(ctx, sql, arguments...)
}

func (c pooledMigrationConnection) Begin(ctx context.Context) (pgx.Tx, error) {
	return c.connection.Begin(ctx)
}

func (c pooledMigrationConnection) Close(ctx context.Context) error {
	return c.connection.Conn().Close(ctx)
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	return migrateWithConnection(ctx, pooledMigrationConnection{connection: connection}, migrations)
}

func migrateWithConnection(ctx context.Context, connection migrationConnection, migrationFS fs.FS) error {
	const lockID int64 = 0x4145505F4D494752
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := connection.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			_ = connection.Close(unlockContext)
		}
	}()
	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
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
		content, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
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
