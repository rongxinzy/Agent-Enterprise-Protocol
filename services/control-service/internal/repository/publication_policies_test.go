package repository

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

func TestGatewayPublicationCapturesDraftOrLatestIntent(t *testing.T) {
	for _, stage := range []string{"draft", "draft-deleted", "draft-lock-error", "draft-read-error", "inherit", "inherit-empty", "inherit-read-error", "inherit-invalid"} {
		t.Run(stage, func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WillReturnRows(sqlmock.NewRows([]string{"operation_id"}))
			failure := errors.New("stop before commit")
			draft := len(stage) > 4 && stage[:5] == "draft"
			lock := mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a")
			if stage == "draft-lock-error" {
				lock.WillReturnError(failure)
			} else {
				lock.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT \* FROM "models"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
				if draft {
					query := mock.ExpectQuery(`SELECT \* FROM "gateway_limits"`).WithArgs("deployment-a")
					if stage == "draft-read-error" {
						query.WillReturnError(failure)
					} else {
						query.WillReturnRows(sqlmock.NewRows([]string{"id", "version", "configuration", "deleted"}).AddRow("shared", 3, []byte(`{"kind":"requests","scopeType":"global","maximum":7,"interval":"minute","enabled":true}`), stage == "draft-deleted"))
					}
				} else {
					query := mock.ExpectQuery(`SELECT s.payload FROM data_plane_publications`).WithArgs("deployment-a")
					if stage == "inherit-read-error" {
						query.WillReturnError(failure)
					} else {
						rows := sqlmock.NewRows([]string{"payload"})
						if stage == "inherit" {
							rows.AddRow([]byte(`{"gatewayLimits":[{"id":"shared","version":2,"configuration":{"maximum":5}}]}`))
						}
						if stage == "inherit-invalid" {
							rows.AddRow([]byte(`{"gatewayLimits":`))
						}
						query.WillReturnRows(rows)
					}
				}
			}
			mock.ExpectRollback()
			called := false
			_, err := store.Deployment("deployment-a").QueueGatewayPublication(t.Context(), "key", "request", "actor", draft, func(_ []Model, items *[]gatewaypolicy.Limit) (PublicationSnapshot, error) {
				called = true
				if stage == "inherit-empty" {
					if items != nil {
						t.Fatal("empty history gained policies")
					}
				} else {
					if items == nil || len(*items) != 1 {
						t.Fatal("missing captured policies")
					}
					item := (*items)[0]
					if draft && (item.Version != 3 || item.Configuration.Maximum != 7 || item.Configuration.Enabled == (stage == "draft-deleted")) {
						t.Fatalf("draft/tombstone changed: %+v", item)
					}
					if !draft && (item.Version != 2 || item.Configuration.Maximum != 5) {
						t.Fatalf("latest intent lost: %+v", item)
					}
				}
				return PublicationSnapshot{}, failure
			})
			if err == nil || called != (stage == "draft" || stage == "draft-deleted" || stage == "inherit" || stage == "inherit-empty") {
				t.Fatalf("stage %s: callback=%t err=%v", stage, called, err)
			}
		})
	}
}

func TestCombinedPublicationReplaySkipsDraftReads(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WillReturnRows(publicationRows("same"))
	mock.ExpectCommit()
	_, err := store.Deployment("deployment-a").QueueGatewayPublication(t.Context(), "key", "same", "actor", true, func([]Model, *[]gatewaypolicy.Limit) (PublicationSnapshot, error) {
		t.Fatal("replay re-read mutable draft")
		return PublicationSnapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
