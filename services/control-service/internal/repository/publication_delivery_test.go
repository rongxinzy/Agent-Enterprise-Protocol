package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPublicationClaimAtomicAndRecoverable(t *testing.T) {
	for _, stage := range []string{"ok", "lock-error", "busy", "empty", "lookup-error", "supersede-error", "outbox-error", "terminal", "leased", "claim-error", "phase-error", "snapshot-error", "history-error", "commit-error"} {
		t.Run(stage, func(t *testing.T) {
			store, mock := newMockStore(t)
			failure := errors.New("injected failure")
			mock.ExpectBegin()
			failed := false
			query := mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).WithArgs("aep:publication:deployment-a")
			if stage == "lock-error" {
				query.WillReturnError(failure)
				failed = true
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(stage != "busy"))
			}
			stopped := stage == "busy" || failed
			if !stopped {
				q := mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WithArgs("deployment-a", 1)
				rows := sqlmock.NewRows([]string{"operation_id", "sequence", "revision", "phase"})
				phase := "queued"
				if stage == "terminal" {
					phase = "projected"
				}
				if stage != "empty" {
					rows.AddRow("11347ddd-5234-4b3d-8936-a45f683e5f56", 5, "rev-5", phase)
				}
				if stage == "lookup-error" {
					q.WillReturnError(failure)
					failed = true
				} else {
					q.WillReturnRows(rows)
				}
				stopped = stage == "empty" || failed
			}
			exec := func(name, pattern string, rows int64) {
				if stopped {
					return
				}
				q := mock.ExpectExec(pattern)
				if stage == name {
					q.WillReturnError(failure)
					failed = true
					stopped = true
				} else {
					q.WillReturnResult(sqlmock.NewResult(0, rows))
				}
			}
			exec("supersede-error", `UPDATE data_plane_publications SET phase='superseded'`, 1)
			exec("outbox-error", `UPDATE data_plane_publication_outbox o SET completed_at`, 1)
			if stage == "terminal" {
				stopped = true
			}
			claimRows := int64(1)
			if stage == "leased" {
				claimRows = 0
			}
			exec("claim-error", `UPDATE data_plane_publication_outbox SET claim_token`, claimRows)
			if stage == "leased" {
				stopped = true
			}
			exec("phase-error", `UPDATE data_plane_publications SET phase='projecting'`, 1)
			for _, read := range []struct{ name, pattern, col, value string }{
				{"snapshot-error", `SELECT payload FROM data_plane_publication_snapshots`, "payload", `{"revision":"rev-5","routes":[]}`},
				{"history-error", `SELECT COALESCE\(jsonb_agg`, "historical_routes", `[]`},
			} {
				if !stopped {
					q := mock.ExpectQuery(read.pattern)
					if stage == read.name {
						q.WillReturnError(failure)
						failed = true
						stopped = true
					} else {
						q.WillReturnRows(sqlmock.NewRows([]string{read.col}).AddRow([]byte(read.value)))
					}
				}
			}
			if failed {
				mock.ExpectRollback()
			} else if stage == "commit-error" {
				mock.ExpectCommit().WillReturnError(failure)
			} else {
				mock.ExpectCommit()
			}
			item, err := store.Deployment("deployment-a").ClaimDataPlanePublication(context.Background())
			wantErr := strings.HasSuffix(stage, "error")
			if (err != nil) != wantErr {
				t.Fatalf("claim = %+v %v", item, err)
			}
			if stage == "ok" {
				if item == nil || item.ClaimToken == "" || string(item.HistoricalRoutes) != "[]" || item.Sequence != 5 {
					t.Fatalf("delivery = %+v", item)
				}
			} else if item != nil {
				t.Fatal("returned uncommitted or unavailable claim")
			}
		})
	}
}

func TestPublicationCompletionFencesClaimAndRollsBack(t *testing.T) {
	for _, stage := range []string{"projected", "retry", "failed", "superseded", "lost", "outbox-error", "phase-error", "commit-error"} {
		t.Run(stage, func(t *testing.T) {
			store, mock := newMockStore(t)
			failure := errors.New("injected failure")
			mock.ExpectBegin()
			outbox := mock.ExpectExec(`UPDATE data_plane_publication_outbox o SET claim_token=NULL`)
			phase, code := "projected", ""
			if stage == "retry" {
				phase = ""
			}
			if stage == "failed" {
				phase = "failed"
				code = "INVALID_OPERATOR_SNAPSHOT"
			}
			if stage == "superseded" {
				phase = "superseded"
			}
			switch stage {
			case "outbox-error":
				outbox.WillReturnError(failure)
			case "lost":
				outbox.WillReturnResult(sqlmock.NewResult(0, 0))
			default:
				outbox.WillReturnResult(sqlmock.NewResult(0, 1))
				q := mock.ExpectExec(`UPDATE data_plane_publications SET phase=`)
				if stage == "phase-error" {
					q.WillReturnError(failure)
				} else {
					q.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			wantErr := stage == "lost" || strings.HasSuffix(stage, "error")
			if stage == "commit-error" {
				mock.ExpectCommit().WillReturnError(failure)
			} else if wantErr {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			err := store.Deployment("deployment-a").FinishDataPlanePublication(context.Background(), "op", "token", phase, code, time.Second)
			if (err != nil) != wantErr {
				t.Fatalf("finish = %v", err)
			}
			if stage == "lost" && !errors.Is(err, ErrPublicationClaimLost) {
				t.Fatal(err)
			}
		})
	}
	store, _ := newMockStore(t)
	if err := store.Deployment("deployment-a").FinishDataPlanePublication(context.Background(), "op", "token", "unknown", "", 0); err == nil {
		t.Fatal("invalid phase accepted")
	}
}
