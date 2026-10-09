package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaysource"
)

const ruleBody = `{"kind":"requests","scopeType":"global","scopeId":null,"modelId":null,"maximum":10,"interval":"minute","enabled":true,"expectedVersion":0}`

func TestGatewayRuleVersionsAndPublication(t *testing.T) {
	a, mock, token := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, a)
	h := New(a).Handler()
	now := time.Now().UTC()
	for _, path := range []string{"limits", "limits/rule-a"} {
		mock.ExpectQuery(`SELECT \* FROM "gateway_limits"`).WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "version", "configuration", "deleted", "updated_at"}).AddRow("deployment-a", "rule-a", 1, []byte(ruleBody), false, now))
		got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/"+path, "")
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	mock.ExpectQuery(`SELECT \* FROM "gateway_limits"`).WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/limits/missing", ""); got.Code != 404 {
		t.Fatal(got.Code)
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`SELECT count`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	pool.ExpectQuery(`INSERT INTO gateway_limits`).WithArgs("deployment-a", "rule-a", pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"version", "updated_at"}).AddRow(int64(1), now))
	pool.ExpectCommit()
	if got := adminRequest(h, token, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", ruleBody); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`UPDATE gateway_limits`).WithArgs("deployment-a", "rule-a", pgxmock.AnyArg(), int64(1)).WillReturnRows(pgxmock.NewRows([]string{"version", "updated_at"}).AddRow(int64(2), now))
	pool.ExpectCommit()
	if got := adminRequest(h, token, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", strings.Replace(ruleBody, `"expectedVersion":0`, `"expectedVersion":1`, 1)); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`UPDATE gateway_limits`).WithArgs("deployment-a", "rule-a", pgxmock.AnyArg(), int64(1)).WillReturnError(pgx.ErrNoRows)
	pool.ExpectRollback()
	if got := adminRequest(h, token, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", strings.Replace(ruleBody, `"expectedVersion":0`, `"expectedVersion":1`, 1)); got.Code != 409 {
		t.Fatal(got.Code)
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectExec(`UPDATE gateway_limits SET deleted=true`).WithArgs("deployment-a", "rule-a", int64(2)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()
	if got := adminRequest(h, token, http.MethodDelete, "/aep/v1/admin/model-gateway/limits/rule-a?expectedVersion=2", ""); got.Code != 204 {
		t.Fatal(got.Code, got.Body.String())
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`SELECT id,version,configuration,updated_at`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"id", "version", "configuration", "updated_at"}).AddRow("rule-a", int64(3), []byte(strings.Replace(ruleBody, `"enabled":true`, `"enabled":false`, 1)), now))
	pool.ExpectQuery(`INSERT INTO gateway_limit_publications`).WithArgs("deployment-a", pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"published_at"}).AddRow(now))
	pool.ExpectCommit()
	got := adminRequest(h, token, http.MethodPost, "/aep/v1/admin/model-gateway/limits/publish", "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"enabled":false`) {
		t.Fatal(got.Code, got.Body.String())
	}
	var p gatewaypolicy.Publication
	if json.Unmarshal(got.Body.Bytes(), &p) != nil || p.Revision != gatewaypolicy.Revision(p.Items) {
		t.Fatal("publication digest")
	}
	for _, status := range []string{"applied", "pending", "error"} {
		observed := p.Revision
		if status == "pending" {
			observed = "old"
		}
		pool.ExpectQuery(`SELECT state,revision,observed_revision`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"state", "revision", "observed_revision"}).AddRow("applied", p.Revision, &observed))
		got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/limits/status", "")
		want := status
		if status == "error" {
			want = "applied"
		}
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"`+want+`"`) || !strings.Contains(got.Body.String(), `"runtimeVerified":false`) {
			t.Fatal(got.Body.String())
		}
	}
	pool.ExpectQuery(`SELECT state,revision,observed_revision`).WithArgs("deployment-a").WillReturnError(pgx.ErrNoRows)
	if got := adminRequest(h, token, http.MethodGet, "/aep/v1/admin/model-gateway/limits/status", ""); !strings.Contains(got.Body.String(), "unpublished") {
		t.Fatal(got.Body.String())
	}
}

func TestGatewayRuleValidationAndDependencies(t *testing.T) {
	a, _, token := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, a)
	h := New(a).Handler()
	for _, body := range []string{"bad", `{}`, strings.Replace(ruleBody, `"maximum":10`, `"maximum":0`, 1)} {
		if got := adminRequest(h, token, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", body); got.Code != 400 {
			t.Fatal(got.Code)
		}
	}
	if got := adminRequest(h, token, http.MethodDelete, "/aep/v1/admin/model-gateway/limits/rule-a?expectedVersion=0", ""); got.Code != 400 {
		t.Fatal(got.Code)
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM teams`).WithArgs("deployment-a", "foreign-team").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectRollback()
	body := strings.Replace(strings.Replace(ruleBody, `"scopeType":"global"`, `"scopeType":"team"`, 1), `"scopeId":null`, `"scopeId":"foreign-team"`, 1)
	if got := adminRequest(h, token, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", body); got.Code != 422 {
		t.Fatal(got.Code, got.Body.String())
	}
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`SELECT count`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(100))
	pool.ExpectRollback()
	if got := adminRequest(h, token, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", ruleBody); got.Code != 409 {
		t.Fatal(got.Code)
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPost} {
		pool.ExpectBegin().WillReturnError(errors.New("database unavailable"))
		path, body := "/aep/v1/admin/model-gateway/limits/rule-a", ruleBody
		if method == http.MethodDelete {
			path += "?expectedVersion=1"
		}
		if method == http.MethodPost {
			path = "/aep/v1/admin/model-gateway/limits/publish"
			body = ""
		}
		if got := adminRequest(h, token, method, path, body); got.Code != 500 {
			t.Fatal(got.Code)
		}
	}
}

func TestNativeQuotaAPI(t *testing.T) {
	a, mock, token := newStoreBackedHTTPApplication(t)
	h := New(a).Handler()
	consumer := gatewaysource.Consumer("deployment-a", "user-a")
	mutations := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer native-admin" {
			t.Error("native source auth")
		}
		if r.FormValue("consumer") != consumer {
			t.Error("forged consumer")
		}
		if r.Method == http.MethodPost {
			mutations++
			_, _ = fmt.Fprint(w, "refresh quota successful")
			return
		}
		_, _ = fmt.Fprintf(w, `{"consumer":%q,"quota":987654321012}`, consumer)
	}))
	defer source.Close()
	a.Config.GatewayQuotaURL = source.URL
	a.Config.GatewayQuotaToken = "native-admin"
	for _, operation := range []struct{ method, path, body string }{{"GET", "", ""}, {"POST", "/refresh", `{"quota":100}`}, {"POST", "/delta", `{"value":-10}`}, {"POST", "/delta", `{"value":0}`}} {
		mock.ExpectQuery(`SELECT \* FROM "users"`).WithArgs("deployment-a", "user-a", 1).WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "status"}).AddRow("user-a", "deployment-a", "active"))
		got := adminRequest(h, token, operation.method, "/aep/v1/admin/model-gateway/quotas/user-a"+operation.path, operation.body)
		if got.Code != 200 || !strings.Contains(got.Body.String(), "987654321012") {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	if mutations != 3 {
		t.Fatal("mutation retry", mutations)
	}
	for _, operation := range []struct{ path, body string }{{"refresh", `{"quota":-1}`}, {"refresh", `{}`}, {"delta", `{"value":1000000000001}`}, {"delta", `{"quota":10}`}} {
		mock.ExpectQuery(`SELECT \* FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("user-a"))
		got := adminRequest(h, token, "POST", "/aep/v1/admin/model-gateway/quotas/user-a/"+operation.path, operation.body)
		if got.Code != 400 {
			t.Fatal(got.Code)
		}
	}
	mock.ExpectQuery(`SELECT \* FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	if got := adminRequest(h, token, "GET", "/aep/v1/admin/model-gateway/quotas/foreign-user", ""); got.Code != 404 {
		t.Fatal(got.Code)
	}
	a.Config.GatewayQuotaURL = ""
	if got := adminRequest(h, token, "GET", "/aep/v1/admin/model-gateway/quotas/user-a", ""); got.Code != 503 {
		t.Fatal(got.Code)
	}
}

func TestNativeGatewayPublicationProtocol(t *testing.T) {
	a, _, _ := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, a)
	a.Config.DataPlaneReconcilerToken = "internal-service"
	h := New(a).Handler()
	now := time.Now().UTC()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AEP-Data-Plane-Token", "internal-service")
		r.Header.Set("X-AEP-Deployment-ID", "deployment-a")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	pool.ExpectQuery(`SELECT revision,items,published_at`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"revision", "items", "published_at"}).AddRow("limits-a", []byte(`[]`), now))
	if got := call("GET", "/internal/data-plane/gateway-limits", ""); got.Code != 200 || !strings.Contains(got.Body.String(), "limits-a") {
		t.Fatal(got.Code, got.Body.String())
	}
	pool.ExpectQuery(`SELECT revision,items,published_at`).WithArgs("deployment-a").WillReturnError(pgx.ErrNoRows)
	if got := call("GET", "/internal/data-plane/gateway-limits", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"items":[]`) {
		t.Fatal(got.Body.String())
	}
	pool.ExpectQuery(`SELECT revision,items,published_at`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"revision", "items", "published_at"}).AddRow("limits-a", []byte(`broken`), now))
	if got := call("GET", "/internal/data-plane/gateway-limits", ""); got.Code != 500 {
		t.Fatal(got.Code)
	}
	for _, body := range []string{`broken`, `{}`, `{"revision":"a","state":"ready"}`} {
		if got := call("PUT", "/internal/data-plane/gateway-limits/status", body); got.Code != 400 {
			t.Fatal(got.Code)
		}
	}
	for _, count := range []int64{1, 0} {
		pool.ExpectExec(`UPDATE gateway_limit_publications`).WithArgs("deployment-a", "limits-a", "applied", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", count))
		got := call("PUT", "/internal/data-plane/gateway-limits/status", `{"revision":"limits-a","state":"applied"}`)
		want := 200
		if count == 0 {
			want = 409
		}
		if got.Code != want {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	pool.ExpectExec(`UPDATE gateway_limit_publications`).WithArgs("deployment-a", "limits-a", "error", pgxmock.AnyArg()).WillReturnError(errors.New("database unavailable"))
	if got := call("PUT", "/internal/data-plane/gateway-limits/status", `{"revision":"limits-a","state":"error"}`); got.Code != 500 {
		t.Fatal(got.Code)
	}
}

func TestNativeRuleDatabaseFailures(t *testing.T) {
	for _, failure := range []string{"lock", "count", "write", "commit", "delete-missing", "publish-query", "publish-json", "publish-write"} {
		t.Run(failure, func(t *testing.T) {
			a, _, token := newStoreBackedHTTPApplication(t)
			pool := attachRuntimeDatabase(t, a)
			h := New(a).Handler()
			pool.ExpectBegin()
			if failure == "lock" {
				pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnError(errors.New("lock unavailable"))
			} else {
				pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			}
			method, path, body := "PUT", "/aep/v1/admin/model-gateway/limits/rule-a", ruleBody
			switch failure {
			case "count":
				pool.ExpectQuery(`SELECT count`).WithArgs("deployment-a").WillReturnError(errors.New("count unavailable"))
			case "write", "commit":
				pool.ExpectQuery(`SELECT count`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
				q := pool.ExpectQuery(`INSERT INTO gateway_limits`).WithArgs("deployment-a", "rule-a", pgxmock.AnyArg())
				if failure == "write" {
					q.WillReturnError(errors.New("write unavailable"))
				} else {
					q.WillReturnRows(pgxmock.NewRows([]string{"version", "updated_at"}).AddRow(int64(1), time.Now().UTC()))
					pool.ExpectCommit().WillReturnError(errors.New("commit unavailable"))
				}
			case "delete-missing":
				method = "DELETE"
				path += "?expectedVersion=1"
				body = ""
				pool.ExpectExec(`UPDATE gateway_limits SET deleted=true`).WithArgs("deployment-a", "rule-a", int64(1)).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			case "publish-query", "publish-json", "publish-write":
				method = "POST"
				path = "/aep/v1/admin/model-gateway/limits/publish"
				body = ""
				q := pool.ExpectQuery(`SELECT id,version,configuration,updated_at`).WithArgs("deployment-a")
				if failure == "publish-query" {
					q.WillReturnError(errors.New("query unavailable"))
				} else if failure == "publish-json" {
					q.WillReturnRows(pgxmock.NewRows([]string{"id", "version", "configuration", "updated_at"}).AddRow("a", int64(1), []byte(`broken`), time.Now().UTC()))
				} else {
					q.WillReturnRows(pgxmock.NewRows([]string{"id", "version", "configuration", "updated_at"}))
					pool.ExpectQuery(`INSERT INTO gateway_limit_publications`).WithArgs("deployment-a", pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("publish unavailable"))
				}
			}
			pool.ExpectRollback()
			got := adminRequest(h, token, method, path, body)
			want := 500
			if failure == "delete-missing" {
				want = 409
			}
			if got.Code != want {
				t.Fatal(got.Code, got.Body.String())
			}
		})
	}
}
