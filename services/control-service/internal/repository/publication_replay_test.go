package repository

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRecoveryReplayRetainsIntentAndAtomicity(t *testing.T) {
	for _, fail := range []string{"", "lock", "lookup", "native", "source", "latest", "not-latest", "references", "pending", "snapshot", "insert", "binding", "operation", "audit", "outbox", "commit"} {
		t.Run(fail, func(t *testing.T) {
			store, mock := newMockStore(t)
			failure := errors.New("unavailable")
			mock.ExpectBegin()
			failed := false
			exec := func(stage, sql string) {
				if failed {
					return
				}
				q := mock.ExpectExec(sql)
				if stage == fail {
					q.WillReturnError(failure)
					failed = true
				} else {
					q.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			query := func(stage, sql string, rows *sqlmock.Rows) {
				if failed {
					return
				}
				q := mock.ExpectQuery(sql)
				if stage == fail {
					q.WillReturnError(failure)
					failed = true
				} else {
					q.WillReturnRows(rows)
				}
			}
			exec("lock", `SELECT pg_advisory_xact_lock`)
			query("lookup", `SELECT \* FROM "data_plane_publications"`, sqlmock.NewRows([]string{"operation_id"}))
			exec("native", `SELECT pg_advisory_xact_lock`)
			rows := func(id string) *sqlmock.Rows {
				return sqlmock.NewRows([]string{"operation_id", "sequence", "revision", "content_hash"}).AddRow(id, 9, "selected", strings.Repeat("a", 64))
			}
			query("source", `SELECT \* FROM "data_plane_publications"`, rows("source"))
			id := "source"
			if fail == "not-latest" {
				id = "newer"
			}
			query("latest", `SELECT \* FROM "data_plane_publications"`, rows(id))
			if fail == "not-latest" {
				failed = true
			}
			query("references", `SELECT EXISTS\(SELECT 1 FROM gateway_reference_invalidations`, sqlmock.NewRows([]string{"exists"}).AddRow(fail == "pending"))
			if fail == "pending" {
				failed = true
			}
			query("snapshot", `SELECT \* FROM "data_plane_publication_snapshots"`, sqlmock.NewRows([]string{"deployment_id", "revision", "content_hash", "payload"}).AddRow("deployment-a", "selected", strings.Repeat("a", 64), []byte(`{"revision":"selected","routes":[]}`)))
			exec("insert", `INSERT INTO "data_plane_publication_snapshots"`)
			query("binding", `SELECT "content_hash"`, sqlmock.NewRows([]string{"content_hash"}).AddRow(strings.Repeat("a", 64)))
			now := time.Now().UTC()
			query("operation", `INSERT INTO "data_plane_publications"`, sqlmock.NewRows([]string{"sequence", "created_at", "updated_at", "idempotency_expires_at"}).AddRow(10, now, now, now.Add(24*time.Hour)))
			exec("audit", `INSERT INTO admin_audit_events`)
			exec("outbox", `INSERT INTO "data_plane_publication_outbox"`)
			switch fail {
			case "":
				mock.ExpectCommit()
			case "commit":
				mock.ExpectCommit().WillReturnError(failure)
			default:
				mock.ExpectRollback()
			}
			value, err := store.Deployment("deployment-a").ReplayDataPlanePublication(t.Context(), "source", "key", "request", "actor")
			if fail == "" {
				if err != nil || value.OperationID == "source" || value.Sequence != 10 || value.Revision != "selected" || value.Phase != "queued" {
					t.Fatalf("replay = %+v %v", value, err)
				}
			} else if err == nil || value.OperationID != "" {
				t.Fatalf("uncommitted replay = %+v %v", value, err)
			}
			if fail == "not-latest" && !errors.Is(err, ErrPublicationNotLatest) {
				t.Fatal(err)
			}
			if fail == "pending" && !errors.Is(err, ErrPublicationReferencesChanged) {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryReplayKeyPrecedesSourceAndReferenceReads(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("aep:publication:deployment-a").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WithArgs("deployment-a", "key", 1).WillReturnRows(publicationRows("request"))
		request := "request"
		if conflict {
			request = "different"
			mock.ExpectRollback()
		} else {
			mock.ExpectCommit()
		}
		value, err := store.Deployment("deployment-a").ReplayDataPlanePublication(t.Context(), "no-longer-latest", "key", request, "actor")
		if conflict {
			if !errors.Is(err, ErrPublicationKeyConflict) {
				t.Fatal(err)
			}
		} else if err != nil || value.OperationID == "" {
			t.Fatalf("replay = %+v %v", value, err)
		}
	}
}
