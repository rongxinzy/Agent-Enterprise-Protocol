package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const pricingQuery = `SELECT \* FROM "model_pricing" WHERE deployment_id=\$1 AND model_id=\$2 LIMIT \$3`
const pricingModelLock = `SELECT "id" FROM "models" WHERE deployment_id=\$1 AND id=\$2 LIMIT \$3 FOR UPDATE`

func TestModelPricingReadRoutes(t *testing.T) {
	for _, scenario := range []string{"unset", "saved", "missing-model", "model-error", "price-error"} {
		t.Run(scenario, func(t *testing.T) {
			application, mock, token := newStoreBackedHTTPApplication(t)
			query := mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).WithArgs("deployment-a", "chat", 1)
			want := 200
			switch scenario {
			case "missing-model":
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
				want = 404
			case "model-error":
				query.WillReturnError(errors.New("database offline"))
				want = 500
			default:
				query.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("chat"))
				read := mock.ExpectQuery(pricingQuery).WithArgs("deployment-a", "chat", 1)
				switch scenario {
				case "unset":
					read.WillReturnRows(sqlmock.NewRows([]string{"model_id"}))
				case "saved":
					read.WillReturnRows(sqlmock.NewRows([]string{"model_id", "version", "pricing", "updated_at"}).AddRow("chat", 5, []byte(`{"currency":"CNY","inputPricePerMillionTokens":"0.000001","outputPricePerMillionTokens":"0"}`), time.Now().UTC()))
				case "price-error":
					read.WillReturnError(errors.New("database offline"))
					want = 500
				}
			}
			r := adminRequest(New(application).Handler(), token, http.MethodGet, "/aep/v1/admin/models/chat/pricing", "")
			if r.Code != want {
				t.Fatalf("HTTP %d: %s", r.Code, r.Body.String())
			}
			if scenario == "unset" && (!strings.Contains(r.Body.String(), `"version":0`) || !strings.Contains(r.Body.String(), `"pricing":null`)) {
				t.Fatal(r.Body.String())
			}
			if scenario == "saved" && !strings.Contains(r.Body.String(), `"inputPricePerMillionTokens":"0.000001"`) {
				t.Fatal(r.Body.String())
			}
		})
	}
}

func TestModelPricingWriteRoutes(t *testing.T) {
	for _, scenario := range []string{"create", "update", "clear", "conflict", "missing-model", "model-error", "read-error", "write-error", "commit-error", "invalid", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			application, mock, token := newStoreBackedHTTPApplication(t)
			body := `{"pricing":{"currency":"USD","inputPricePerMillionTokens":"0.000001","outputPricePerMillionTokens":"0"},"expectedVersion":1}`
			want := 200
			switch scenario {
			case "invalid":
				body = `{"pricing":null}`
				want = 400
			case "malformed":
				body = `not-json`
				want = 400
			default:
				mock.ExpectBegin()
				lock := mock.ExpectQuery(pricingModelLock).WithArgs("deployment-a", "chat", 1)
				switch scenario {
				case "missing-model":
					lock.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = 404
				case "model-error":
					lock.WillReturnError(errors.New("database offline"))
					want = 500
				default:
					lock.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("chat"))
					read := mock.ExpectQuery(pricingQuery).WithArgs("deployment-a", "chat", 1)
					switch scenario {
					case "read-error":
						read.WillReturnError(errors.New("database offline"))
						want = 500
					case "conflict":
						read.WillReturnRows(sqlmock.NewRows([]string{"model_id", "version"}).AddRow("chat", 2))
						want = 409
					default:
						if scenario == "create" {
							body = strings.Replace(body, `"expectedVersion":1`, `"expectedVersion":0`, 1)
							read.WillReturnRows(sqlmock.NewRows([]string{"model_id"}))
							mock.ExpectExec(`INSERT INTO "model_pricing"`).WillReturnResult(sqlmock.NewResult(0, 1))
						} else {
							read.WillReturnRows(sqlmock.NewRows([]string{"model_id", "version"}).AddRow("chat", 1))
							var update *sqlmock.ExpectedExec
							if scenario == "clear" {
								update = mock.ExpectExec(`UPDATE "model_pricing" SET .* WHERE deployment_id=\$3 AND model_id=\$4`).WithArgs(sqlmock.AnyArg(), int64(2), "deployment-a", "chat")
							} else {
								update = mock.ExpectExec(`UPDATE "model_pricing" SET .* WHERE deployment_id=\$4 AND model_id=\$5`).WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(2), "deployment-a", "chat")
							}
							if scenario == "write-error" {
								update.WillReturnError(errors.New("database offline"))
								want = 500
							} else {
								update.WillReturnResult(sqlmock.NewResult(0, 1))
							}
						}
						if scenario == "clear" {
							body = `{"pricing":null,"expectedVersion":1}`
						}
					}
				}
				if scenario == "commit-error" {
					mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
					want = 500
				} else if want != 200 {
					mock.ExpectRollback()
				} else {
					mock.ExpectCommit()
				}
			}
			r := adminRequest(New(application).Handler(), token, http.MethodPut, "/aep/v1/admin/models/chat/pricing", body)
			if r.Code != want {
				t.Fatalf("HTTP %d, want %d: %s", r.Code, want, r.Body.String())
			}
			if scenario == "clear" && !strings.Contains(r.Body.String(), `"pricing":null`) {
				t.Fatal(r.Body.String())
			}
			if scenario == "conflict" && !strings.Contains(r.Body.String(), "MODEL_PRICING_VERSION_CONFLICT") {
				t.Fatal(r.Body.String())
			}
		})
	}
}
