package repository

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPublicationObservationInputsAreScopedAndReadOnly(t *testing.T) {
	for _, stage := range []string{"ok", "foreign", "missing", "snapshot-error", "history-error"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := newMockStore(t)
			value := DataPlanePublication{DeploymentID: "deployment-a", Sequence: 7, Revision: "rev-7", ContentHash: "hash"}
			if stage == "foreign" {
				value.DeploymentID = "other"
			} else {
				q := mock.ExpectQuery(`SELECT payload FROM data_plane_publication_snapshots WHERE deployment_id=\$1 AND revision=\$2 AND content_hash=\$3`).WithArgs("deployment-a", "rev-7", "hash")
				switch stage {
				case "snapshot-error":
					q.WillReturnError(errors.New("unavailable"))
				case "missing":
					q.WillReturnRows(sqlmock.NewRows([]string{"payload"}))
				default:
					q.WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow([]byte(`{"revision":"rev-7","routes":[]}`)))
				}
				if stage == "ok" || stage == "history-error" {
					q := mock.ExpectQuery(`SELECT COALESCE\(jsonb_agg`).WithArgs("deployment-a", 7)
					if stage == "history-error" {
						q.WillReturnError(errors.New("unavailable"))
					} else {
						q.WillReturnRows(sqlmock.NewRows([]string{"historical_routes"}).AddRow([]byte(`[]`)))
					}
				}
			}
			got, err := s.Deployment("deployment-a").ReadDataPlanePublicationInputs(t.Context(), value)
			if (err != nil) != (stage != "ok") {
				t.Fatalf("inputs = %+v %v", got, err)
			}
			if stage == "ok" && (got.Sequence != 7 || got.Revision != "rev-7" || got.ContentHash != "hash" || string(got.HistoricalRoutes) != "[]" || got.ClaimToken != "") {
				t.Fatalf("inputs lost immutable identity: %+v", got)
			}
		})
	}
}

func TestLatestPublicationFence(t *testing.T) {
	for _, stage := range []string{"latest", "superseded", "error"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := newMockStore(t)
			q := mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM data_plane_publications p`).WithArgs("deployment-a", "op", 7)
			if stage == "error" {
				q.WillReturnError(errors.New("unavailable"))
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(stage == "latest"))
			}
			latest, err := s.Deployment("deployment-a").IsLatestDataPlanePublication(t.Context(), DataPlanePublication{OperationID: "op", Sequence: 7})
			if latest != (stage == "latest") || (err != nil) != (stage == "error") {
				t.Fatalf("latest = %t %v", latest, err)
			}
		})
	}
}
