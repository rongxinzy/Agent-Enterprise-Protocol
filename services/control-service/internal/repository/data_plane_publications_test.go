package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func publicationRows(requestHash string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows([]string{"deployment_id", "operation_id", "revision", "content_hash", "request_hash", "phase", "created_at", "updated_at", "idempotency_expires_at"}).
		AddRow("deployment-a", "11347ddd-5234-4b3d-8936-a45f683e5f56", "release-1", strings.Repeat("a", 64), requestHash, "queued", now, now, now.Add(24*time.Hour))
}

func TestPublicationReplayDoesNotReadCatalogOrCreateAudit(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("aep:publication:deployment-a").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WithArgs("deployment-a", "key-hash", 1).WillReturnRows(publicationRows("original"))
		requestHash := "original"
		if conflict {
			requestHash = "different"
			mock.ExpectRollback()
		} else {
			mock.ExpectCommit()
		}
		value, err := store.Deployment("deployment-a").QueueDataPlanePublication(context.Background(), "key-hash", requestHash, "actor", func([]Model) (PublicationSnapshot, error) {
			t.Fatal("idempotent replay must not rederive from the current catalog")
			return PublicationSnapshot{}, nil
		})
		if conflict {
			if !errors.Is(err, ErrPublicationKeyConflict) || value.OperationID != "" {
				t.Fatalf("conflict = %+v %v", value, err)
			}
		} else if err != nil || value.Revision != "release-1" {
			t.Fatalf("replay = %+v %v", value, err)
		}
	}
}

func TestPublicationQueueAtomicFailures(t *testing.T) {
	for _, fail := range []string{"", "lock", "lookup", "models", "derive", "snapshot", "binding", "revision", "operation", "audit", "outbox", "commit"} {
		t.Run(fail, func(t *testing.T) {
			store, mock := newMockStore(t)
			failure := errors.New("injected failure")
			mock.ExpectBegin()
			stageFailed := false
			exec := func(stage, query string) {
				if stageFailed {
					return
				}
				expect := mock.ExpectExec(query)
				if stage == fail {
					expect.WillReturnError(failure)
					stageFailed = true
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			query := func(stage, sql string, rows *sqlmock.Rows) {
				if stageFailed {
					return
				}
				expect := mock.ExpectQuery(sql)
				if stage == fail {
					expect.WillReturnError(failure)
					stageFailed = true
				} else {
					expect.WillReturnRows(rows)
				}
			}
			exec("lock", `SELECT pg_advisory_xact_lock`)
			query("lookup", `SELECT \* FROM "data_plane_publications"`, sqlmock.NewRows([]string{"operation_id"}))
			query("models", `SELECT \* FROM "models" WHERE deployment_id = \$1 ORDER BY id`, sqlmock.NewRows([]string{"id", "deployment_id"}).AddRow("model-a", "deployment-a"))
			if fail == "derive" {
				stageFailed = true
			}
			exec("snapshot", `INSERT INTO "data_plane_publication_snapshots"`)
			hash := strings.Repeat("a", 64)
			if fail == "revision" {
				hash = strings.Repeat("b", 64)
			}
			query("binding", `SELECT "content_hash" FROM "data_plane_publication_snapshots"`, sqlmock.NewRows([]string{"content_hash"}).AddRow(hash))
			if fail == "revision" {
				stageFailed = true
			}
			now := time.Now().UTC()
			query("operation", `INSERT INTO "data_plane_publications"`, sqlmock.NewRows([]string{"sequence", "created_at", "updated_at", "idempotency_expires_at"}).AddRow(1, now, now, now.Add(24*time.Hour)))
			exec("audit", `INSERT INTO admin_audit_events`)
			exec("outbox", `INSERT INTO "data_plane_publication_outbox"`)
			if fail == "commit" {
				mock.ExpectCommit().WillReturnError(failure)
			} else if fail == "" {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			value, err := store.Deployment("deployment-a").QueueDataPlanePublication(context.Background(), strings.Repeat("c", 64), strings.Repeat("d", 64), "actor", func(models []Model) (PublicationSnapshot, error) {
				if len(models) != 1 || models[0].DeploymentID != "deployment-a" {
					t.Fatalf("catalog = %+v", models)
				}
				if fail == "derive" {
					return PublicationSnapshot{}, failure
				}
				return PublicationSnapshot{Revision: "release-1", ContentHash: strings.Repeat("a", 64), Payload: json.RawMessage(`{"revision":"release-1","routes":[]}`)}, nil
			})
			if fail == "" {
				if err != nil || value.Phase != "queued" || value.Sequence != 1 {
					t.Fatalf("queued = %+v %v", value, err)
				}
				if _, err := uuid.Parse(value.OperationID); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || value.OperationID != "" {
				t.Fatalf("%s returned uncommitted operation: %+v %v", fail, value, err)
			}
		})
	}
}

func TestPublicationReadIsDeploymentScoped(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(`SELECT \* FROM "data_plane_publications" WHERE deployment_id = \$1 AND operation_id = \$2`).WithArgs("deployment-a", "other-operation", 1).WillReturnRows(sqlmock.NewRows([]string{"operation_id"}))
	if _, err := store.Deployment("deployment-a").GetDataPlanePublication(context.Background(), "other-operation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign operation = %v", err)
	}
}
