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
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const publicationPath = "/aep/v1/admin/data-plane/publications"
const publicationTestID = "11347ddd-5234-4b3d-8936-a45f683e5f56"

func publicationHTTP(handler http.Handler, token, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-AEP-Protocol-Version", "1.0")
	request.Header.Set("Authorization", "Bearer "+token)
	for _, key := range keys {
		request.Header.Add("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestPublicationRejectsInvalidRequestsWithoutDatabaseWrites(t *testing.T) {
	for _, test := range []struct {
		body, key string
		status    int
		code      string
	}{
		{`{}`, "", 400, "INVALID_IDEMPOTENCY_KEY"}, {`{}`, "key with space", 400, "INVALID_IDEMPOTENCY_KEY"}, {`{}`, strings.Repeat("a", 129), 400, "INVALID_IDEMPOTENCY_KEY"},
		{"", "valid", 400, "INVALID_REQUEST"}, {`null`, "valid", 400, "INVALID_REQUEST"}, {`[]`, "valid", 400, "INVALID_REQUEST"}, {`{`, "valid", 400, "INVALID_REQUEST"},
		{`{} {}`, "valid", 400, "INVALID_REQUEST"}, {`{"revision":"one","revision":"two"}`, "valid", 400, "INVALID_REQUEST"},
		{`{"revision":null}`, "valid", 422, "INVALID_DATA_PLANE_STATE"}, {`{"revision":12}`, "valid", 422, "INVALID_DATA_PLANE_STATE"},
		{`{"revision":"   "}`, "valid", 422, "INVALID_DATA_PLANE_STATE"}, {`{"revision":"` + strings.Repeat("r", 201) + `"}`, "valid", 422, "INVALID_DATA_PLANE_STATE"},
		{`{"routes":[]}`, "valid", 400, "INVALID_REQUEST"}, {strings.Repeat(" ", 4097) + `{}`, "valid", 400, "INVALID_REQUEST"},
	} {
		t.Run(test.body+test.key, func(t *testing.T) {
			a, _, token := newStoreBackedHTTPApplication(t)
			a.Config.GatewayPublicationsEnabled = true
			response := publicationHTTP(New(a).Handler(), token, "POST", publicationPath, test.body, test.key)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	a, _, token := newStoreBackedHTTPApplication(t)
	a.Config.GatewayPublicationsEnabled = true
	response := publicationHTTP(New(a).Handler(), token, "POST", publicationPath, `{}`, "one", "two")
	if response.Code != 400 {
		t.Fatalf("duplicate headers = %d", response.Code)
	}
}

func TestPublicationDefaultDisabledAndAuthenticated(t *testing.T) {
	a, _, token := newStoreBackedHTTPApplication(t)
	for _, method := range []string{"GET", "POST"} {
		path := publicationPath
		if method == "GET" {
			path += "/" + publicationTestID
		}
		response := publicationHTTP(New(a).Handler(), token, method, path, `{}`, "key")
		if response.Code != 503 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("disabled = %d %s", response.Code, response.Body.String())
		}
		response = publicationHTTP(New(a).Handler(), "", method, path, `{}`, "key")
		if response.Code != 401 {
			t.Fatalf("unauthenticated = %d", response.Code)
		}
	}
}

func TestPublicationReplayNormalizesBodyAndHidesInternalFields(t *testing.T) {
	for _, test := range []struct{ body, normalized string }{{`{}`, `{}`}, {`{"revision":" release-1 "}`, `{"revision":"release-1"}`}} {
		a, mock, token := newStoreBackedHTTPApplication(t)
		a.Config.GatewayPublicationsEnabled = true
		now := time.Now().UTC()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WithArgs("deployment-a", publicationDigest([]byte("replay-key")), 1).
			WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "operation_id", "request_hash", "idempotency_key_hash", "sequence", "revision", "content_hash", "phase", "created_at", "updated_at", "idempotency_expires_at"}).
				AddRow("deployment-a", publicationTestID, publicationDigest([]byte(test.normalized)), "never-expose-key-hash", 42, "release-1", strings.Repeat("a", 64), "queued", now, now, now.Add(24*time.Hour)))
		mock.ExpectCommit()
		response := publicationHTTP(New(a).Handler(), token, "POST", publicationPath, test.body, "replay-key")
		if response.Code != 202 || response.Header().Get("Location") != publicationPath+"/"+publicationTestID || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("replay = %d %s", response.Code, response.Body.String())
		}
		var value publicationView
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if len(value.Conditions) != 3 {
			t.Fatal("missing conditions")
		}
		for _, condition := range value.Conditions {
			if condition.Status != "Unknown" || condition.ObservedRevision != nil {
				t.Fatalf("fabricated observation %+v", condition)
			}
		}
		for _, hidden := range []string{"requestHash", "idempotencyKeyHash", "sequence", "payload", "never-expose-key-hash"} {
			if strings.Contains(response.Body.String(), hidden) {
				t.Fatalf("internal field leaked: %s", hidden)
			}
		}
	}
}

func TestPublicationQueryNotFoundAndUnavailable(t *testing.T) {
	for _, failure := range []bool{false, true} {
		a, mock, token := newStoreBackedHTTPApplication(t)
		a.Config.GatewayPublicationsEnabled = true
		query := mock.ExpectQuery(`SELECT \* FROM "data_plane_publications" WHERE deployment_id = \$1 AND operation_id = \$2`).WithArgs("deployment-a", publicationTestID, 1)
		status := 404
		if failure {
			query.WillReturnError(errors.New("sensitive database diagnostic"))
			status = 503
		} else {
			query.WillReturnRows(sqlmock.NewRows([]string{"operation_id"}))
		}
		response := publicationHTTP(New(a).Handler(), token, "GET", publicationPath+"/"+publicationTestID, "")
		if response.Code != status || strings.Contains(response.Body.String(), "sensitive") {
			t.Fatalf("query = %d %s", response.Code, response.Body.String())
		}
	}
	a, _, token := newStoreBackedHTTPApplication(t)
	a.Config.GatewayPublicationsEnabled = true
	response := publicationHTTP(New(a).Handler(), token, "GET", publicationPath+"/not-a-uuid", "")
	if response.Code != 404 {
		t.Fatalf("invalid UUID = %d", response.Code)
	}
}

func TestPublicationReauthorizesEveryReplay(t *testing.T) {
	a, mock, _ := newStoreBackedHTTPApplication(t)
	a.Config.GatewayPublicationsEnabled = true
	pool := attachRuntimeDatabase(t, a)
	token, _, err := a.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-user", false, false, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{true, false} {
		pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "user-a", "data_plane.write").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(allowed))
		if allowed {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WillReturnRows(sqlmock.NewRows([]string{"operation_id", "request_hash"}).AddRow(publicationTestID, publicationDigest([]byte(`{}`))))
			mock.ExpectCommit()
		}
		response := publicationHTTP(New(a).Handler(), token, "POST", publicationPath, `{}`, "existing-key")
		expected := 403
		if allowed {
			expected = 202
		}
		if response.Code != expected {
			t.Fatalf("reauthorized=%t: %d %s", allowed, response.Code, response.Body.String())
		}
	}
}

func TestPublicationCreateAndErrorMapping(t *testing.T) {
	for _, outcome := range []string{"created", "conflict", "database", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			a, mock, token := newStoreBackedHTTPApplication(t)
			a.Config.GatewayPublicationsEnabled = true
			now := time.Now().UTC()
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
			lookup := mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`)
			status := 202
			switch outcome {
			case "conflict":
				lookup.WillReturnRows(sqlmock.NewRows([]string{"request_hash"}).AddRow("another-request"))
				status = 409
			case "database":
				lookup.WillReturnError(errors.New("private database error"))
				status = 503
			default:
				lookup.WillReturnRows(sqlmock.NewRows([]string{"operation_id"}))
				rows := catalogModelRows(now)
				if outcome == "invalid" {
					rows = sqlmock.NewRows(modelHTTPColumns())
					for i := 0; i < 501; i++ {
						rows.AddRow("deployment-a", fmt.Sprintf("model-%d", i), "Model", "gateway", "openai-compatible", "/v1", "chat", nil, nil, `{text}`, nil, nil, false, true, "unknown", nil, nil, nil, now, now)
					}
					status = 422
				}
				catalogModelsQuery(mock, rows)
				if outcome == "created" {
					mock.ExpectExec(`INSERT INTO "data_plane_publication_snapshots"`).WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectQuery(`SELECT "content_hash"`).WillReturnRows(sqlmock.NewRows([]string{"content_hash"}).AddRow(dataPlaneHash(mustPublicationState(t))))
					mock.ExpectQuery(`INSERT INTO "data_plane_publications"`).WillReturnRows(sqlmock.NewRows([]string{"sequence", "created_at", "updated_at", "idempotency_expires_at"}).AddRow(1, now, now, now.Add(24*time.Hour)))
					mock.ExpectExec(`INSERT INTO admin_audit_events`).WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectExec(`INSERT INTO "data_plane_publication_outbox"`).WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			if outcome == "created" {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			response := publicationHTTP(New(a).Handler(), token, "POST", publicationPath, `{}`, "new-key")
			if response.Code != status || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("create = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func mustPublicationState(t *testing.T) dataPlaneDesiredStateWrite {
	t.Helper()
	// Same catalog data as catalogModelRows, expressed only as expected wire
	// routes. This ensures publication shares the established route/hash format.
	state, ok := normalizeDataPlaneState(dataPlaneDesiredStateWrite{Revision: "catalog-", Routes: []dataPlaneRoute{
		{ModelID: "chat-b", Enabled: true, Endpoint: "/v1", UpstreamModel: "provider-b-chat", Protocol: "openai-compatible", ProviderType: "openai"},
		{ModelID: "chat-a", Enabled: true, Endpoint: "http://provider-a/v1", UpstreamModel: "provider-a-chat", Protocol: "openai-compatible", ProviderType: "openai", CredentialRef: &dataPlaneSecretReference{Name: "aep-credential-credential-a", Key: "api-key"}},
		{ModelID: "chat-c", Enabled: false, Endpoint: "http://provider-c/v1", UpstreamModel: "provider-c-chat", Protocol: "openai-compatible", ProviderType: "openai", CredentialRef: &dataPlaneSecretReference{Name: "aep-credential-credential-c", Key: "api-key"}},
	}})
	if !ok {
		t.Fatal("invalid fixture")
	}
	raw, _ := json.Marshal(state.Routes)
	state.Revision = "catalog-" + publicationDigest(raw)
	return state
}
