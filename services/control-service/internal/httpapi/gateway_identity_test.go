package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestInternalGatewayIdentity(t *testing.T) {
	a, mock, _ := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, a)
	a.Config.GatewayLicenseStatusToken = "service-token"
	r := httptest.NewRequest(http.MethodGet, "/internal/gateway/identity", nil)
	r.Header.Set("X-AEP-Gateway-Token", "service-token")
	r.Header.Set("X-AEP-Deployment-ID", "deployment-a")
	r.Header.Set("X-AEP-User-ID", "user-a")
	r.Header.Set("X-AEP-Session-ID", "session-a")
	r.Header.Set("X-AEP-Model-ID", "model-a")
	pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "user-a").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("model-a"))
	mock.ExpectQuery(`SELECT "b"."role_id" FROM user_role_bindings AS b JOIN roles AS r`).WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("role-a").AddRow("role-b"))
	mock.ExpectQuery(`SELECT "b"."team_id" FROM user_team_bindings AS b JOIN teams AS t`).WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("team-a"))
	w := httptest.NewRecorder()
	New(a).Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"roleIds":["role-a","role-b"]`) || !strings.Contains(w.Body.String(), `"consumer":"aep.`) {
		t.Fatal(w.Code, w.Body.String())
	}
	r.Header.Del("X-AEP-Gateway-Token")
	w = httptest.NewRecorder()
	New(a).Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r.Header.Set("X-AEP-Gateway-Token", "service-token")
	r.Header.Del("X-AEP-Model-ID")
	w = httptest.NewRecorder()
	New(a).Handler().ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	r.Header.Set("X-AEP-Model-ID", "unassigned")
	pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "user-a").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("model-a"))
	w = httptest.NewRecorder()
	New(a).Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestInternalGatewayIdentityDependencyFailures(t *testing.T) {
	for _, failure := range []string{"scopes", "roles", "teams"} {
		a, mock, _ := newStoreBackedHTTPApplication(t)
		pool := attachRuntimeDatabase(t, a)
		a.Config.GatewayLicenseStatusToken = "service-token"
		r := httptest.NewRequest(http.MethodGet, "/internal/gateway/identity", nil)
		r.Header.Set("X-AEP-Gateway-Token", "service-token")
		r.Header.Set("X-AEP-Deployment-ID", "deployment-a")
		r.Header.Set("X-AEP-User-ID", "user-a")
		r.Header.Set("X-AEP-Session-ID", "session-a")
		r.Header.Set("X-AEP-Model-ID", "model-a")
		if failure == "scopes" {
			pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "user-a").WillReturnError(fmt.Errorf("dependency unavailable"))
		} else {
			pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "user-a").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("model-a"))
			if failure == "roles" {
				mock.ExpectQuery(`SELECT "b"."role_id"`).WithArgs("deployment-a", "user-a").WillReturnError(fmt.Errorf("roles unavailable"))
			} else {
				mock.ExpectQuery(`SELECT "b"."role_id"`).WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}))
				mock.ExpectQuery(`SELECT "b"."team_id"`).WithArgs("deployment-a", "user-a").WillReturnError(fmt.Errorf("teams unavailable"))
			}
		}
		w := httptest.NewRecorder()
		New(a).Handler().ServeHTTP(w, r)
		if w.Code != 500 {
			t.Fatal(failure, w.Code, w.Body.String())
		}
	}
}
