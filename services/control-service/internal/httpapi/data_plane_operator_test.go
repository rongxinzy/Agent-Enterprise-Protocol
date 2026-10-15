package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/publicationrelay"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func TestOperatorModeFencesLegacyPathsAfterAuthentication(t *testing.T) {
	for _, relay := range []bool{false, true} {
		for _, endpoint := range []struct{ method, path string }{
			{"PUT", "/aep/v1/admin/data-plane/desired-state"},
			{"POST", "/aep/v1/admin/data-plane/publish"},
			{"POST", "/aep/v1/admin/model-gateway/limits/publish"},
			{"GET", "/aep/v1/admin/model-gateway/limits/status"},
			{"GET", "/internal/data-plane/desired-state"},
			{"PUT", "/internal/data-plane/status"},
			{"GET", "/internal/data-plane/gateway-limits"},
			{"PUT", "/internal/data-plane/gateway-limits/status"},
		} {
			t.Run(endpoint.method+endpoint.path+strconv.FormatBool(relay), func(t *testing.T) {
				a, _, token := newStoreBackedHTTPApplication(t)
				a.Config.GatewayOperatorMode = !relay
				a.Config.GatewayRelayEnabled = relay
				a.Config.DataPlaneReconcilerToken = "disposable-service-token"
				handler := New(a).Handler()
				for _, authenticated := range []bool{false, true} {
					request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`invalid`))
					request.Header.Set("X-AEP-Protocol-Version", "1.0")
					if authenticated {
						request.Header.Set("Authorization", "Bearer "+token)
						request.Header.Set("X-AEP-Data-Plane-Token", a.Config.DataPlaneReconcilerToken)
						request.Header.Set("X-AEP-Deployment-ID", "deployment-a")
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					want := 401
					if authenticated {
						want = 503
					}
					if response.Code != want || (authenticated && (!strings.Contains(response.Body.String(), "LEGACY_GATEWAY_PUBLICATION_DISABLED") || response.Header().Get("Cache-Control") != "no-store")) {
						t.Fatalf("legacy response = %d %s", response.Code, response.Body.String())
					}
				}
			})
		}
	}
}

func TestOperatorCompatibilityReadsSelectedIntent(t *testing.T) {
	for _, endpoint := range []string{"desired-state", "status"} {
		for _, stage := range []string{"ready", "applied", "queued", "no-reader", "observe-error", "superseded", "empty", "db-error", "snapshot-error", "bad-hash", "catalog-error"} {
			t.Run(endpoint+"/"+stage, func(t *testing.T) {
				a, mock, token := newStoreBackedHTTPApplication(t)
				a.Config.GatewayOperatorMode = true // relay paused; observation still works
				now := time.Now().UTC()
				payload := []byte(`{"revision":"rev-42","routes":[],"gatewayLimits":[]}`)
				hash := publicationDigest(payload)
				payload = []byte(`{ "gatewayLimits": [], "routes": [], "revision": "rev-42" }`)
				if stage == "bad-hash" {
					hash = strings.Repeat("a", 64)
				}
				phase := "projected"
				if stage == "queued" {
					phase = "queued"
				}
				latest := func(changed bool) {
					q := mock.ExpectQuery(`SELECT \* FROM "data_plane_publications" WHERE deployment_id = \$1 ORDER BY sequence DESC LIMIT \$2`).WithArgs("deployment-a", 1)
					rows := sqlmock.NewRows([]string{"deployment_id", "operation_id", "sequence", "revision", "content_hash", "phase", "created_at"})
					if stage == "db-error" {
						q.WillReturnError(errors.New("sensitive diagnostic"))
						return
					}
					if stage != "empty" {
						id := publicationTestID
						if changed {
							id = "new-operation"
						}
						rows.AddRow("deployment-a", id, 42, "rev-42", hash, phase, now)
					}
					q.WillReturnRows(rows)
				}
				inputs := func(fail bool) {
					q := mock.ExpectQuery(`SELECT payload FROM data_plane_publication_snapshots`).WithArgs("deployment-a", "rev-42", hash)
					if fail {
						q.WillReturnError(errors.New("sensitive snapshot"))
						return
					}
					q.WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(payload))
					mock.ExpectQuery(`SELECT COALESCE\(jsonb_agg`).WithArgs("deployment-a", 42).WillReturnRows(sqlmock.NewRows([]string{"historical_routes"}).AddRow([]byte(`[]`)))
				}
				a.PublicationObserver = testPublicationObserver(func(_ context.Context, item repository.PublicationDelivery) ([]publicationrelay.Condition, error) {
					if stage == "observe-error" {
						return nil, errors.New("sensitive Kubernetes error")
					}
					conditions := publicationrelay.UnknownConditions(now, "NotObserved")
					for i := range conditions {
						conditions[i].ObservedRevision = &item.Revision
						conditions[i].Status = "True"
					}
					if stage == "applied" {
						conditions[2].Status = "Unknown"
					}
					return conditions, nil
				})
				if stage == "no-reader" {
					a.PublicationObserver = nil
				}
				latest(false)
				failed := stage == "db-error" || stage == "snapshot-error" || stage == "bad-hash"
				if stage != "empty" && stage != "db-error" {
					inputs(stage == "snapshot-error")
				}
				if !failed {
					if endpoint == "status" {
						q := mock.ExpectQuery(`SELECT \* FROM "models"`).WithArgs("deployment-a")
						if stage == "catalog-error" {
							q.WillReturnError(errors.New("sensitive catalog"))
							failed = true
						} else {
							q.WillReturnRows(sqlmock.NewRows([]string{"id"}))
						}
						if !failed && stage != "empty" && stage != "no-reader" && phase == "projected" {
							inputs(false)
							if stage != "observe-error" {
								mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM data_plane_publications p`).WithArgs("deployment-a", publicationTestID, 42).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
							}
						}
					}
					if !failed {
						latest(stage == "superseded")
					}
				}
				response := adminRequest(New(a).Handler(), token, "GET", "/aep/v1/admin/data-plane/"+endpoint, "")
				want := 200
				if failed || stage == "superseded" {
					want = 503
				}
				if response.Code != want || strings.Contains(response.Body.String(), "sensitive") || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("read = %d %s", response.Code, response.Body.String())
				}
				if want == 200 && endpoint == "status" {
					var view dataPlaneStatusView
					if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
						t.Fatal(err)
					}
					expected := "pending"
					switch stage {
					case "ready":
						expected = "ready"
					case "applied":
						expected = "applying"
					}
					if view.State != expected || (stage != "empty" && (view.ContentHash == nil || *view.ContentHash != hash)) {
						t.Fatalf("status = %+v", view)
					}
				}
			})
		}
	}
}

func TestOperatorStatusRequiresAppliedEvidenceForReady(t *testing.T) {
	now := time.Now().UTC()
	value := repository.DataPlanePublication{OperationID: publicationTestID, Revision: "r", ContentHash: "hash", Phase: "projected"}
	for _, test := range []struct {
		name, phase, applied, ready, observed, expected string
	}{
		{"applied without ready", "projected", "True", "Unknown", "r", "applying"},
		{"ready without applied", "projected", "Unknown", "True", "r", "pending"},
		{"wrong revision", "projected", "True", "True", "old", "pending"},
		{"rejected", "projected", "False", "Unknown", "r", "error"},
		{"delivery failed", "failed", "Unknown", "Unknown", "r", "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value.Phase = test.phase
			conditions := publicationrelay.UnknownConditions(now, "Test")
			conditions[1].Status, conditions[2].Status = test.applied, test.ready
			for i := range conditions {
				conditions[i].ObservedRevision = &test.observed
			}
			status := operatorStatus(value, conditions)
			if status.State != test.expected || (test.expected == "pending" && status.ObservedRevision != nil) {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}
