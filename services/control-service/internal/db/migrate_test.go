package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const migrationLockID int64 = 0x4145505F4D494752

func newMigrationMock(t *testing.T) pgxmock.PgxConnIface {
	t.Helper()
	connection, err := pgxmock.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return connection
}

func expectMigrationLock(connection pgxmock.PgxConnIface) {
	connection.ExpectExec(`SELECT pg_advisory_lock\(\$1\)`).WithArgs(migrationLockID).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
}

func expectMigrationUnlock(connection pgxmock.PgxConnIface) {
	connection.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).WithArgs(migrationLockID).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
}

func expectMigrationTable(connection pgxmock.PgxConnIface) {
	connection.ExpectExec(`CREATE TABLE IF NOT EXISTS schema_migrations`).
		WillReturnResult(pgconn.NewCommandTag("CREATE TABLE"))
}

func TestMigrateRejectsClosedPool(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://aep:aep@127.0.0.1:1/aep")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	if err := Migrate(context.Background(), pool); err == nil {
		t.Fatal("Migrate() accepted a closed connection pool")
	}
}

func TestMigrateWithConnectionAppliesPendingFilesInOrder(t *testing.T) {
	connection := newMigrationMock(t)
	migrationFS := fstest.MapFS{
		"migrations/002_second.sql":       &fstest.MapFile{Data: []byte("SELECT 'second';")},
		"migrations/001_first.sql":        &fstest.MapFile{Data: []byte("SELECT 'first';")},
		"migrations/README.txt":           &fstest.MapFile{Data: []byte("ignored")},
		"migrations/nested/003_third.sql": &fstest.MapFile{Data: []byte("SELECT 'nested';")},
	}
	expectMigrationLock(connection)
	expectMigrationTable(connection)
	connection.ExpectQuery(`SELECT EXISTS`).WithArgs("001_first.sql").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	connection.ExpectQuery(`SELECT EXISTS`).WithArgs("002_second.sql").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	connection.ExpectBegin()
	connection.ExpectExec(`SELECT 'second';`).WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	connection.ExpectExec(`INSERT INTO schema_migrations`).WithArgs("002_second.sql").
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	connection.ExpectCommit()
	expectMigrationUnlock(connection)

	if err := migrateWithConnection(context.Background(), connection, migrationFS); err != nil {
		t.Fatalf("migrateWithConnection() error = %v", err)
	}
}

func TestMigrateWithConnectionSetupFailures(t *testing.T) {
	t.Run("lock", func(t *testing.T) {
		connection := newMigrationMock(t)
		connection.ExpectExec(`SELECT pg_advisory_lock\(\$1\)`).WithArgs(migrationLockID).
			WillReturnError(errors.New("lock unavailable"))
		err := migrateWithConnection(context.Background(), connection, fstest.MapFS{})
		if err == nil || !strings.Contains(err.Error(), "acquire migration lock: lock unavailable") {
			t.Fatalf("migrateWithConnection() error = %v", err)
		}
	})

	t.Run("migration table", func(t *testing.T) {
		connection := newMigrationMock(t)
		expectMigrationLock(connection)
		connection.ExpectExec(`CREATE TABLE IF NOT EXISTS schema_migrations`).
			WillReturnError(errors.New("schema unavailable"))
		expectMigrationUnlock(connection)
		if err := migrateWithConnection(context.Background(), connection, fstest.MapFS{}); err == nil || err.Error() != "schema unavailable" {
			t.Fatalf("migrateWithConnection() error = %v", err)
		}
	})

	t.Run("migration directory", func(t *testing.T) {
		connection := newMigrationMock(t)
		expectMigrationLock(connection)
		expectMigrationTable(connection)
		expectMigrationUnlock(connection)
		if err := migrateWithConnection(context.Background(), connection, fstest.MapFS{}); err == nil {
			t.Fatal("migrateWithConnection() accepted a missing migration directory")
		}
	})
}

func TestMigrateWithConnectionTransactionFailures(t *testing.T) {
	migrationFS := fstest.MapFS{
		"migrations/001_test.sql": &fstest.MapFile{Data: []byte("SELECT 'migration';")},
	}

	tests := []struct {
		name      string
		configure func(pgxmock.PgxConnIface)
		want      string
	}{
		{
			name: "applied lookup",
			configure: func(connection pgxmock.PgxConnIface) {
				connection.ExpectQuery(`SELECT EXISTS`).WithArgs("001_test.sql").WillReturnError(errors.New("lookup failed"))
			},
			want: "lookup failed",
		},
		{
			name: "begin",
			configure: func(connection pgxmock.PgxConnIface) {
				connection.ExpectQuery(`SELECT EXISTS`).WithArgs("001_test.sql").
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
				connection.ExpectBegin().WillReturnError(errors.New("begin failed"))
			},
			want: "begin failed",
		},
		{
			name: "migration statement",
			configure: func(connection pgxmock.PgxConnIface) {
				connection.ExpectQuery(`SELECT EXISTS`).WithArgs("001_test.sql").
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
				connection.ExpectBegin()
				connection.ExpectExec(`SELECT 'migration';`).WillReturnError(errors.New("statement failed"))
				connection.ExpectRollback()
			},
			want: "apply migration 001_test.sql: statement failed",
		},
		{
			name: "version record",
			configure: func(connection pgxmock.PgxConnIface) {
				connection.ExpectQuery(`SELECT EXISTS`).WithArgs("001_test.sql").
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
				connection.ExpectBegin()
				connection.ExpectExec(`SELECT 'migration';`).WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
				connection.ExpectExec(`INSERT INTO schema_migrations`).WithArgs("001_test.sql").
					WillReturnError(errors.New("version insert failed"))
				connection.ExpectRollback()
			},
			want: "apply migration 001_test.sql: version insert failed",
		},
		{
			name: "commit",
			configure: func(connection pgxmock.PgxConnIface) {
				connection.ExpectQuery(`SELECT EXISTS`).WithArgs("001_test.sql").
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
				connection.ExpectBegin()
				connection.ExpectExec(`SELECT 'migration';`).WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
				connection.ExpectExec(`INSERT INTO schema_migrations`).WithArgs("001_test.sql").
					WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
				connection.ExpectCommit().WillReturnError(errors.New("commit failed"))
			},
			want: "commit failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := newMigrationMock(t)
			expectMigrationLock(connection)
			expectMigrationTable(connection)
			test.configure(connection)
			expectMigrationUnlock(connection)
			err := migrateWithConnection(context.Background(), connection, migrationFS)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("migrateWithConnection() error = %v, want detail %q", err, test.want)
			}
		})
	}
}

func TestMigrateWithConnectionClosesOnUnlockFailure(t *testing.T) {
	connection := newMigrationMock(t)
	expectMigrationLock(connection)
	expectMigrationTable(connection)
	connection.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).WithArgs(migrationLockID).
		WillReturnError(errors.New("unlock failed"))
	connection.ExpectClose()

	migrationFS := fstest.MapFS{
		"migrations/README.txt": &fstest.MapFile{Data: []byte("ignored")},
	}
	if err := migrateWithConnection(context.Background(), connection, migrationFS); err != nil {
		t.Fatalf("migrateWithConnection() error = %v", err)
	}
}
