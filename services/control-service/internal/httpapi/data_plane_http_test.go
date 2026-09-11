package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAdminDataPlaneDesiredStateLifecycle(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	publishedAt := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)

	payload := `{"revision":"rev-2","routes":[{"modelId":"z-model","enabled":true,"endpoint":"http://gateway/v1","upstreamModel":"z-upstream","protocol":"openai-compatible","providerType":"deepseek","credentialRef":{"name":"provider-secret","key":"api-key"}},{"modelId":"a-model","enabled":false,"endpoint":"http://gateway/v1","upstreamModel":"a-upstream","protocol":"openai-compatible"}]}`
	pool.ExpectQuery(`INSERT INTO data_plane_desired_states`).
		WithArgs("deployment-a", "rev-2", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"published_at"}).AddRow(publishedAt))
	pool.ExpectExec(`INSERT INTO data_plane_statuses`).
		WithArgs("deployment-a", 2).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	created := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/data-plane/desired-state", payload)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"revision":"rev-2"`) || !strings.Contains(created.Body.String(), `"modelId":"a-model"`) || !strings.Contains(created.Body.String(), `"modelId":"z-model"`) || !strings.Contains(created.Body.String(), `"contentHash"`) {
		t.Fatalf("put desired state = %d %s", created.Code, created.Body.String())
	}

	pool.ExpectQuery(`SELECT deployment_id,revision,routes,content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").WillReturnError(errors.New("database unavailable"))
	failedRead := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-plane/desired-state", "")
	if failedRead.Code != http.StatusInternalServerError || !strings.Contains(failedRead.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("desired state database error = %d %s", failedRead.Code, failedRead.Body.String())
	}
}

func TestAdminDataPlaneReadsDefaultStates(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()

	pool.ExpectQuery(`SELECT deployment_id,revision,routes,content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"deployment_id", "revision", "routes", "content_hash", "published_at"}))
	state := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-plane/desired-state", "")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"deploymentId":"deployment-a"`) || !strings.Contains(state.Body.String(), `"routes":[]`) || !strings.Contains(state.Body.String(), `"revision":""`) || !strings.Contains(state.Body.String(), `"contentHash"`) {
		t.Fatalf("default desired state = %d %s", state.Code, state.Body.String())
	}

	pool.ExpectQuery(`SELECT state,observed_revision,content_hash,last_applied_at,error_code,message,resource_count FROM data_plane_statuses`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"state", "observed_revision", "content_hash", "last_applied_at", "error_code", "message", "resource_count"}))
	status := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-plane/status", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state":"pending"`) || !strings.Contains(status.Body.String(), `"resourceCount":0`) {
		t.Fatalf("default data-plane status = %d %s", status.Code, status.Body.String())
	}
}

func TestInternalDataPlaneStatusLifecycle(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	application.Config.DataPlaneReconcilerToken = "reconciler-secret"
	handler := New(application).Handler()

	pool.ExpectExec(`INSERT INTO data_plane_statuses`).
		WithArgs("deployment-a", "ready", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), 2).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	updated := internalDataPlaneRequestWithToken(handler, http.MethodPut, "/internal/data-plane/status", `{"state":"ready","observedRevision":"rev-2","contentHash":"hash-2","resourceCount":2}`, "reconciler-secret")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"state":"ready"`) || !strings.Contains(updated.Body.String(), `"resourceCount":2`) {
		t.Fatalf("put internal status = %d %s", updated.Code, updated.Body.String())
	}

	invalid := internalDataPlaneRequestWithToken(handler, http.MethodPut, "/internal/data-plane/status", `{"state":"unknown"}`, "reconciler-secret")
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), `"code":"INVALID_DATA_PLANE_STATUS"`) {
		t.Fatalf("invalid internal status = %d %s", invalid.Code, invalid.Body.String())
	}

	tooLong := internalDataPlaneRequestWithToken(handler, http.MethodPut, "/internal/data-plane/status", `{"state":"error","message":"`+strings.Repeat("x", 2001)+`"}`, "reconciler-secret")
	if tooLong.Code != http.StatusBadRequest || !strings.Contains(tooLong.Body.String(), `"code":"INVALID_DATA_PLANE_STATUS"`) {
		t.Fatalf("long internal status message = %d %s", tooLong.Code, tooLong.Body.String())
	}

	unauthorized := adminRequest(handler, "", http.MethodPut, "/internal/data-plane/status", `{"state":"ready"}`)
	if unauthorized.Code != http.StatusUnauthorized || !strings.Contains(unauthorized.Body.String(), `"code":"INTERNAL_AUTH_REQUIRED"`) {
		t.Fatalf("internal status without service token = %d %s", unauthorized.Code, unauthorized.Body.String())
	}
}

func internalDataPlaneRequestWithToken(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-AEP-Data-Plane-Token", token)
	request.Header.Set("X-AEP-Deployment-ID", "deployment-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
