package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

func credentialHTTPColumns() []string {
	return []string{
		"deployment_id", "id", "name", "service", "type", "delivery_mode", "encrypted_value",
		"nonce", "key_id", "masked_value", "enabled", "created_at", "updated_at", "rotated_at",
	}
}

func credentialRuntimeColumns() []string {
	return []string{
		"id", "name", "service", "type", "delivery_mode", "encrypted_value", "nonce",
		"key_id", "masked_value", "enabled", "created_at", "updated_at", "rotated_at",
	}
}

func TestAdminCredentialLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "credentials"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/credentials", `{"name":" Provider ","service":"deepseek","type":"api_key","deliveryMode":"server_only","value":"provider-secret-value","enabled":true}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"name":"Provider"`) || !strings.Contains(created.Body.String(), `"maskedValue":"****alue"`) || strings.Contains(created.Body.String(), "provider-secret-value") {
		t.Fatalf("create Credential = %d %s", created.Code, created.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(sqlmock.NewRows(credentialHTTPColumns()).AddRow("deployment-a", "credential-a", "Provider", "deepseek", "api_key", "server_only", []byte("ciphertext"), []byte("nonce"), "key-a", "****alue", true, now, now, now))
	got := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/credentials/credential-a", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"id":"credential-a"`) || strings.Contains(got.Body.String(), "ciphertext") {
		t.Fatalf("get Credential = %d %s", got.Code, got.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "credentials" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(sqlmock.NewRows(credentialHTTPColumns()).AddRow("deployment-a", "credential-a", "Renamed Provider", "proxy", "api_key", "client", []byte("ciphertext"), []byte("nonce"), "key-a", "****alue", false, now, now, now))
	updated := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/credentials/credential-a", `{"name":" Renamed Provider ","service":" proxy ","deliveryMode":"client","enabled":false}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"name":"Renamed Provider"`) || !strings.Contains(updated.Body.String(), `"deliveryMode":"client"`) || !strings.Contains(updated.Body.String(), `"enabled":false`) {
		t.Fatalf("update Credential = %d %s", updated.Code, updated.Body.String())
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "credential-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "credentials" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(sqlmock.NewRows(credentialHTTPColumns()).AddRow("deployment-a", "credential-a", "Renamed Provider", "proxy", "api_key", "client", []byte("rotated-ciphertext"), []byte("rotated-nonce"), "key-b", "****alue", false, now, now, now))
	rotated := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/credentials/credential-a/rotate", `{"value":"rotated-secret-value"}`)
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `"maskedValue":"****alue"`) || strings.Contains(rotated.Body.String(), "rotated-secret-value") || strings.Contains(rotated.Body.String(), "rotated-ciphertext") {
		t.Fatalf("rotate Credential = %d %s", rotated.Code, rotated.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "credential_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "credential_id", "subject_type", "subject_id", "created_at"}).
			AddRow("assignment-a", "deployment-a", "credential-a", "team", "engineering", now))
	assignments := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/credential-assignments", "")
	if assignments.Code != http.StatusOK || !strings.Contains(assignments.Body.String(), `"resourceId":"credential-a"`) || !strings.Contains(assignments.Body.String(), `"type":"team"`) {
		t.Fatalf("list Credential assignments = %d %s", assignments.Code, assignments.Body.String())
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "credential-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "credential_assignments"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	assigned := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/credential-assignments", `{"credentialId":"credential-a","subject":{"type":"role","id":"operator"}}`)
	if assigned.Code != http.StatusCreated || !strings.Contains(assigned.Body.String(), `"resourceId":"credential-a"`) || !strings.Contains(assigned.Body.String(), `"type":"role"`) {
		t.Fatalf("create Credential assignment = %d %s", assigned.Code, assigned.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "credential_assignments" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "assignment-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	revoked := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/credential-assignments/assignment-a", "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("delete Credential assignment = %d %s", revoked.Code, revoked.Body.String())
	}
}

func TestAdminCredentialDeleteErrors(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "credential-in-use").WillReturnError(&pgconn.PgError{Code: "23503"})
	mock.ExpectRollback()
	inUse := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/credentials/credential-in-use", "")
	if inUse.Code != http.StatusConflict || !strings.Contains(inUse.Body.String(), `"code":"CREDENTIAL_IN_USE"`) {
		t.Fatalf("delete referenced Credential = %d %s", inUse.Code, inUse.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "missing").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	missing := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/credentials/missing", "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("delete missing Credential = %d %s", missing.Code, missing.Body.String())
	}
}

func TestUserCredentialListAndResolve(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	envelope, err := application.Credentials.Seal(context.Background(), []byte("client-secret"), credential.AssociatedData("deployment-a", "credential-client"))
	if err != nil {
		t.Fatal(err)
	}

	pool.ExpectQuery(`SELECT c\.id,c\.name,c\.service`).WithArgs("deployment-a", "user-a").
		WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow("credential-client", "Client key", "provider", "api_key", "client", envelope.Ciphertext, envelope.Nonce, envelope.KeyID, "****cret", true, now, now, now))
	listed := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/credentials", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"credential-client"`) || !strings.Contains(listed.Body.String(), `"maskedValue":"****cret"`) || strings.Contains(listed.Body.String(), "client-secret") {
		t.Fatalf("list user Credentials = %d %s", listed.Code, listed.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-client").
		WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow("credential-client", "Client key", "provider", "api_key", "client", envelope.Ciphertext, envelope.Nonce, envelope.KeyID, "****cret", true, now, now, now))
	pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "credential-client").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "credential-client", "user-a", "session-user", "connect provider", "resolved", nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	resolved := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/credentials/credential-client/resolve", `{"purpose":" connect provider "}`)
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"value":"client-secret"`) || resolved.Header().Get("Cache-Control") != "no-store" || resolved.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("resolve user Credential = %d %s", resolved.Code, resolved.Body.String())
	}
}

func TestUserCredentialResolutionDenialsAreAudited(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	invalidPurpose := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/credentials/credential-a/resolve", `{"purpose":""}`)
	if invalidPurpose.Code != http.StatusBadRequest || !strings.Contains(invalidPurpose.Body.String(), `"code":"INVALID_PURPOSE"`) {
		t.Fatalf("invalid purpose = %d %s", invalidPurpose.Code, invalidPurpose.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "credential-server").
		WillReturnRows(pgxmock.NewRows(credentialRuntimeColumns()).AddRow("credential-server", "Server key", "provider", "api_key", "server_only", []byte("cipher"), []byte("nonce"), "key-a", "****cret", true, now, now, now))
	pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "credential-server", "user-a", "session-user", "use provider", "denied", "server_only").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	serverOnly := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/credentials/credential-server/resolve", `{"purpose":"use provider"}`)
	if serverOnly.Code != http.StatusForbidden || !strings.Contains(serverOnly.Body.String(), `"code":"CREDENTIAL_SERVER_ONLY"`) {
		t.Fatalf("server-only Credential = %d %s", serverOnly.Code, serverOnly.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id,name,service,type,delivery_mode`).WithArgs("deployment-a", "missing").WillReturnError(pgx.ErrNoRows)
	pool.ExpectExec(`INSERT INTO credential_resolution_audit`).WithArgs(pgxmock.AnyArg(), "deployment-a", "missing", "user-a", "session-user", "use provider", "denied", "not_found").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	missing := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/credentials/missing/resolve", `{"purpose":"use provider"}`)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing Credential = %d %s", missing.Code, missing.Body.String())
	}
}
