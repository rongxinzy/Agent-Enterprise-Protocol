package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

func newDataPlanePublishApplication(t *testing.T) (*app.App, pgxmock.PgxPoolIface, sqlmock.Sqlmock, string) {
	t.Helper()
	application, storeMock, adminToken := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	return application, pool, storeMock, adminToken
}

func catalogModelsQuery(storeMock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	storeMock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a").WillReturnRows(rows)
}

func catalogModelRows(now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(modelHTTPColumns()).
		AddRow("deployment-a", "chat-b", "Chat B", "gateway", "openai-compatible", "/v1", "provider-b-chat", nil, nil, `{text}`, nil, nil, false, true, "unknown", nil, nil, now, now).
		AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://provider-a/v1", "provider-a-chat", nil, "credential-a", `{text}`, nil, 8192, false, true, "unknown", nil, nil, now, now).
		AddRow("deployment-a", "chat-c", "Chat C", "gateway", "openai-compatible", "http://provider-c/v1", "provider-c-chat", nil, "credential-c", `{text}`, nil, nil, false, false, "unknown", nil, nil, now, now).
		AddRow("deployment-a", "local-a", "Local A", "local", "openai-compatible", nil, nil, "local-ref", nil, `{text}`, nil, nil, false, true, "unknown", nil, nil, now, now)
}

func TestPublishDataPlaneRoutesDerivesCatalogState(t *testing.T) {
	application, pool, storeMock, adminToken := newDataPlanePublishApplication(t)
	handler := New(application).Handler()
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	publishedAt := now.Add(time.Hour)

	catalogModelsQuery(storeMock, catalogModelRows(now))
	pool.ExpectQuery(`SELECT content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"content_hash", "published_at"}))
	pool.ExpectQuery(`INSERT INTO data_plane_desired_states`).
		WithArgs("deployment-a", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"published_at"}).AddRow(publishedAt))
	pool.ExpectExec(`INSERT INTO data_plane_statuses`).
		WithArgs("deployment-a", 3).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	published := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{}`)
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	var state struct {
		Revision    string           `json:"revision"`
		Routes      []dataPlaneRoute `json:"routes"`
		ContentHash string           `json:"contentHash"`
		PublishedAt time.Time        `json:"publishedAt"`
	}
	if err := json.Unmarshal(published.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	if !strings.HasPrefix(state.Revision, "catalog-") || len(state.ContentHash) != 64 || !state.PublishedAt.Equal(publishedAt) {
		t.Fatalf("publish response = %s", published.Body.String())
	}
	if len(state.Routes) != 3 || state.Routes[0].ModelID != "chat-a" || state.Routes[1].ModelID != "chat-b" || state.Routes[2].ModelID != "chat-c" || state.Routes[2].Enabled {
		t.Fatalf("derived routes = %s", published.Body.String())
	}
	reference := state.Routes[0].CredentialRef
	if reference == nil || reference.Name != "aep-credential-credential-a" || reference.Key != "api-key" || reference.Namespace == nil || *reference.Namespace != "higress-system" {
		t.Fatalf("derived credentialRef = %s", published.Body.String())
	}
	if state.Routes[0].ProviderType != "openai" || state.Routes[1].CredentialRef != nil {
		t.Fatalf("derived route normalization = %s", published.Body.String())
	}
}

func TestPublishDataPlaneRoutesIsIdempotentAndAdvancesRevision(t *testing.T) {
	application, pool, storeMock, adminToken := newDataPlanePublishApplication(t)
	handler := New(application).Handler()
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	publishedAt := now.Add(time.Hour)
	catalog := []modelRecord{catalogModel("chat-a", true, "gateway", "http://provider-a/v1", "provider-a-chat", "credential-a")}
	derived, ok := deriveCatalogState(catalog, "")
	if !ok {
		t.Fatal("catalog derivation failed")
	}
	hash := dataPlaneHash(derived)

	catalogModelsQuery(storeMock, sqlmock.NewRows(modelHTTPColumns()).
		AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://provider-a/v1", "provider-a-chat", nil, "credential-a", `{text}`, nil, nil, false, true, "unknown", nil, nil, now, now))
	pool.ExpectQuery(`SELECT content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"content_hash", "published_at"}))
	pool.ExpectQuery(`INSERT INTO data_plane_desired_states`).
		WithArgs("deployment-a", derived.Revision, pgxmock.AnyArg(), hash).
		WillReturnRows(pgxmock.NewRows([]string{"published_at"}).AddRow(publishedAt))
	pool.ExpectExec(`INSERT INTO data_plane_statuses`).
		WithArgs("deployment-a", 1).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	first := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{}`)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"revision":"`+derived.Revision+`"`) || !strings.Contains(first.Body.String(), `"contentHash":"`+hash+`"`) {
		t.Fatalf("first publish = %d %s", first.Code, first.Body.String())
	}

	// Same catalog: the stored hash matches, so no write happens and the
	// stored publication time is returned unchanged.
	catalogModelsQuery(storeMock, sqlmock.NewRows(modelHTTPColumns()).
		AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://provider-a/v1", "provider-a-chat", nil, "credential-a", `{text}`, nil, nil, false, true, "unknown", nil, nil, now, now))
	pool.ExpectQuery(`SELECT content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"content_hash", "published_at"}).AddRow(hash, publishedAt))
	repeated := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{}`)
	if repeated.Code != http.StatusOK || !strings.Contains(repeated.Body.String(), `"revision":"`+derived.Revision+`"`) || !strings.Contains(repeated.Body.String(), publishedAt.Format(time.RFC3339Nano)) {
		t.Fatalf("idempotent publish = %d %s", repeated.Code, repeated.Body.String())
	}

	// A catalog change advances the content-addressed revision.
	catalogModelsQuery(storeMock, catalogModelRows(now))
	pool.ExpectQuery(`SELECT content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"content_hash", "published_at"}).AddRow(hash, publishedAt))
	pool.ExpectQuery(`INSERT INTO data_plane_desired_states`).
		WithArgs("deployment-a", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"published_at"}).AddRow(publishedAt.Add(time.Hour)))
	pool.ExpectExec(`INSERT INTO data_plane_statuses`).
		WithArgs("deployment-a", 3).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	advanced := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{}`)
	if advanced.Code != http.StatusOK || strings.Contains(advanced.Body.String(), `"revision":"`+derived.Revision+`"`) || !strings.Contains(advanced.Body.String(), `"revision":"catalog-`) {
		t.Fatalf("advancing publish = %d %s", advanced.Code, advanced.Body.String())
	}
}

func TestPublishDataPlaneRoutesValidatesAndAuthorizes(t *testing.T) {
	application, pool, storeMock, adminToken := newDataPlanePublishApplication(t)
	handler := New(application).Handler()

	oversized := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{"revision":"`+strings.Repeat("r", 201)+`"}`)
	if oversized.Code != http.StatusBadRequest || !strings.Contains(oversized.Body.String(), `"code":"INVALID_DATA_PLANE_STATE"`) {
		t.Fatalf("oversized revision = %d %s", oversized.Code, oversized.Body.String())
	}

	// An empty catalog publishes an empty route set, replacing any stored routes.
	catalogModelsQuery(storeMock, sqlmock.NewRows(modelHTTPColumns()))
	pool.ExpectQuery(`SELECT content_hash,published_at FROM data_plane_desired_states`).
		WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"content_hash", "published_at"}))
	pool.ExpectQuery(`INSERT INTO data_plane_desired_states`).
		WithArgs("deployment-a", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"published_at"}).AddRow(time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)))
	pool.ExpectExec(`INSERT INTO data_plane_statuses`).
		WithArgs("deployment-a", 0).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	empty := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{}`)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"routes":[]`) {
		t.Fatalf("empty catalog publish = %d %s", empty.Code, empty.Body.String())
	}

	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-user", "deployment-a", "session-delegated", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectQuery(`SELECT EXISTS \(`).
		WithArgs("deployment-a", "delegated-user", "data_plane.write").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	forbidden := adminRequest(handler, token, http.MethodPost, "/aep/v1/admin/data-plane/publish", `{}`)
	if forbidden.Code != http.StatusForbidden || !strings.Contains(forbidden.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("publish without data_plane.write = %d %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestDataPlaneStatusReportsCatalogComparison(t *testing.T) {
	application, pool, storeMock, adminToken := newDataPlanePublishApplication(t)
	handler := New(application).Handler()
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)

	pool.ExpectQuery(`SELECT state,observed_revision,content_hash,last_applied_at,error_code,message,resource_count FROM data_plane_statuses`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"state", "observed_revision", "content_hash", "last_applied_at", "error_code", "message", "resource_count"}).
			AddRow("ready", nil, nil, nil, nil, nil, 2))
	catalogModelsQuery(storeMock, sqlmock.NewRows(modelHTTPColumns()).
		AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://provider-a/v1", "provider-a-chat", nil, "credential-a", `{text}`, nil, nil, false, true, "unknown", nil, nil, now, now).
		AddRow("deployment-a", "chat-b", "Chat B", "gateway", "openai-compatible", "/v1", "provider-b-chat", nil, nil, `{text}`, nil, nil, false, true, "unknown", nil, nil, now, now).
		AddRow("deployment-a", "chat-c", "Chat C", "gateway", "openai-compatible", "http://provider-c/v1", "provider-c-chat", nil, nil, `{text}`, nil, nil, false, false, "unknown", nil, nil, now, now))
	storedRoutes := []byte(`[{"modelId":"chat-a","enabled":true,"endpoint":"http://provider-a/v1","upstreamModel":"stale-upstream","protocol":"openai-compatible","providerType":"openai","credentialRef":{"name":"legacy-secret","key":"api-key","namespace":"higress-system"}},{"modelId":"ghost","enabled":true,"endpoint":"/v1","upstreamModel":"ghost","protocol":"openai-compatible","providerType":"openai"}]`)
	pool.ExpectQuery(`SELECT routes FROM data_plane_desired_states`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"routes"}).AddRow(storedRoutes))

	status := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/data-plane/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	var view struct {
		State             string `json:"state"`
		CatalogComparison struct {
			Missing    []string `json:"missing"`
			Extra      []string `json:"extra"`
			Mismatched []struct {
				ModelID string   `json:"modelId"`
				Fields  []string `json:"fields"`
			} `json:"mismatched"`
		} `json:"catalogComparison"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if view.State != "ready" || len(view.CatalogComparison.Missing) != 2 || view.CatalogComparison.Missing[0] != "chat-b" || view.CatalogComparison.Missing[1] != "chat-c" || len(view.CatalogComparison.Extra) != 1 || view.CatalogComparison.Extra[0] != "ghost" {
		t.Fatalf("catalog comparison = %s", status.Body.String())
	}
	if len(view.CatalogComparison.Mismatched) != 1 || view.CatalogComparison.Mismatched[0].ModelID != "chat-a" || strings.Join(view.CatalogComparison.Mismatched[0].Fields, ",") != "upstreamModel,credentialRef" {
		t.Fatalf("catalog mismatches = %s", status.Body.String())
	}
	if strings.Contains(status.Body.String(), "provider-secret") {
		t.Fatalf("status leaked secret material: %s", status.Body.String())
	}
}
