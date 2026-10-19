package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func assetHTTPColumns() []string {
	return []string{
		"deployment_id", "id", "type_id", "name", "description", "org_team_id", "location_id", "owner_user_id",
		"undertakers", "spec", "credential_refs", "capabilities_override", "status",
		"health_state", "health_checked_at", "health_detail", "source", "created_at", "updated_at",
	}
}

func assetHTTPRows(id, name, status string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(assetHTTPColumns()).
		AddRow("deployment-a", id, "ssh_server", name, nil, nil, nil, nil,
			`[]`,
			`{"host":"10.0.0.5","port":22,"username":"ops","authMethod":"private_key"}`,
			`{"default":"credential-a"}`, `{}`, status,
			nil, nil, nil, "manual", now, now)
}

func TestAdminAssetPermissionMapping(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		want   string
	}{
		{method: http.MethodGet, path: "/aep/v1/admin/asset-types", want: "assets.read"},
		{method: http.MethodGet, path: "/aep/v1/admin/assets", want: "assets.read"},
		{method: http.MethodGet, path: "/aep/v1/admin/assets/asset-a", want: "assets.read"},
		{method: http.MethodGet, path: "/aep/v1/admin/assets/asset-a/bindings", want: "assets.read"},
		{method: http.MethodPost, path: "/aep/v1/admin/assets", want: "assets.write"},
		{method: http.MethodPatch, path: "/aep/v1/admin/assets/asset-a", want: "assets.write"},
		{method: http.MethodDelete, path: "/aep/v1/admin/assets/asset-a", want: "assets.write"},
		{method: http.MethodPost, path: "/aep/v1/admin/assets/asset-a/bindings", want: "assets.write"},
		{method: http.MethodDelete, path: "/aep/v1/admin/assets/asset-a/bindings/binding-a", want: "assets.write"},
	} {
		permissions := requiredAdminPermission(test.method, test.path)
		if len(permissions) != 1 || permissions[0] != test.want {
			t.Errorf("%s %s = %v, want [%s]", test.method, test.path, permissions, test.want)
		}
	}
}

func TestAdminAssetLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	// Create drafts an asset: transaction with the insert and its audit row.
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "assets"`).WillReturnResult(sqlmock.NewResult(1, 1))
	expectAuditInsert(mock)
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets",
		`{"typeId":"ssh_server","name":"Bastion","spec":{"host":"10.0.0.5","port":22,"username":"ops","authMethod":"private_key"}}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"status":"draft"`) ||
		!strings.Contains(created.Body.String(), `"key":"exec"`) || !strings.Contains(created.Body.String(), `"risk":"high"`) {
		t.Fatalf("create asset = %d %s", created.Code, created.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "draft"))
	got := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/assets/asset-a", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"typeId":"ssh_server"`) ||
		!strings.Contains(got.Body.String(), `"health":null`) {
		t.Fatalf("get asset = %d %s", got.Code, got.Body.String())
	}

	// Patch: read-before for audit, then update + re-read + audit in one tx.
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "draft"))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "assets" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion Renamed", "active"))
	expectAuditInsert(mock)
	mock.ExpectCommit()
	patched := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/assets/asset-a", `{"name":"Bastion Renamed"}`)
	if patched.Code != http.StatusOK || !strings.Contains(patched.Body.String(), `"name":"Bastion Renamed"`) {
		t.Fatalf("patch asset = %d %s", patched.Code, patched.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 51).WillReturnRows(assetHTTPRows("asset-a", "Bastion Renamed", "active"))
	listed := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/assets", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"asset-a"`) {
		t.Fatalf("list assets = %d %s", listed.Code, listed.Body.String())
	}

	// Delete: read-before + delete + audit in one transaction.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion Renamed", "active"))
	mock.ExpectExec(`DELETE FROM "assets" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "asset-a").WillReturnResult(sqlmock.NewResult(0, 1))
	expectAuditInsert(mock)
	mock.ExpectCommit()
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/assets/asset-a", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete asset = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestAdminAssetValidation(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	for _, test := range []struct {
		name string
		body string
		code string
	}{
		{"unknown type", `{"typeId":"robot_arm","name":"Arm","spec":{}}`, "UNKNOWN_ASSET_TYPE"},
		{"missing host", `{"typeId":"ssh_server","name":"B","spec":{"username":"ops","authMethod":"password"}}`, "INVALID_ASSET_SPEC"},
		{"bad port", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","port":99999,"username":"ops","authMethod":"password"}}`, "INVALID_ASSET_SPEC"},
		{"unknown spec field", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password","bastion":"x"}}`, "INVALID_ASSET_SPEC"},
		{"bad auth method", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"token"}}`, "INVALID_ASSET_SPEC"},
		{"empty name", `{"typeId":"ssh_server","name":"  ","spec":{"host":"h","username":"ops","authMethod":"password"}}`, "INVALID_ASSET"},
		{"bad status", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"status":"live"}`, "INVALID_ASSET"},
		{"active without credential", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"status":"active"}`, "INVALID_ASSET"},
		{"bad undertaker", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"undertakers":[{"type":"user","id":"u"}]}`, "INVALID_ASSET"},
		{"unknown capability override", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"capabilitiesOverride":{"reboot":{}}}`, "INVALID_ASSET"},
		{"bad override risk", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"capabilitiesOverride":{"exec":{"risk":"extreme"}}}`, "INVALID_ASSET"},
		{"bad credential ref field", `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"credentialRefs":{"backup":"x"}}`, "INVALID_ASSET"},
	} {
		response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets", test.body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("%s: create asset = %d %s, want 400 %s", test.name, response.Code, response.Body.String(), test.code)
		}
	}

	types := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/asset-types", "")
	if types.Code != http.StatusOK || !strings.Contains(types.Body.String(), `"id":"ssh_server"`) ||
		!strings.Contains(types.Body.String(), `"requiresCredential":true`) {
		t.Fatalf("list asset types = %d %s", types.Code, types.Body.String())
	}
}

func TestAdminAssetCredentialRefRejected(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	body := `{"typeId":"ssh_server","name":"B","spec":{"host":"h","username":"ops","authMethod":"password"},"credentialRefs":{"default":"credential-a"}}`

	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(sqlmock.NewRows(credentialHTTPColumns()))
	missing := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets", body)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "INVALID_CREDENTIAL_REF") {
		t.Fatalf("missing credential ref = %d %s", missing.Code, missing.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(credentialRows("credential-a", "Provider", "ssh", "client", "ciphertext", "nonce", "key-a", "****alue", true))
	clientMode := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets", body)
	if clientMode.Code != http.StatusBadRequest || !strings.Contains(clientMode.Body.String(), "INVALID_CREDENTIAL_REF") {
		t.Fatalf("client-delivery credential ref = %d %s", clientMode.Code, clientMode.Body.String())
	}
}

func TestAdminAssetBindingLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	// Create: asset existence check, then binding + audit in one tx.
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "active"))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "asset_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	expectAuditInsert(mock)
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/bindings",
		`{"subject":{"type":"companion","id":"demo"},"capabilityAllowlist":["exec"]}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"type":"companion"`) ||
		!strings.Contains(created.Body.String(), `"capabilityAllowlist":["exec"]`) {
		t.Fatalf("create binding = %d %s", created.Code, created.Body.String())
	}

	// Same subject again collides with the unique index.
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "active"))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "asset_bindings"`).WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()
	duplicate := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/bindings",
		`{"subject":{"type":"companion","id":"demo"}}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "ASSET_BINDING_EXISTS") {
		t.Fatalf("duplicate binding = %d %s", duplicate.Code, duplicate.Body.String())
	}

	// Unknown capability in the allowlist is rejected before any write.
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "active"))
	badAllowlist := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/bindings",
		`{"subject":{"type":"companion","id":"demo"},"capabilityAllowlist":["reboot"]}`)
	if badAllowlist.Code != http.StatusBadRequest || !strings.Contains(badAllowlist.Body.String(), "INVALID_ASSET_BINDING") {
		t.Fatalf("bad allowlist = %d %s", badAllowlist.Code, badAllowlist.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND asset_id = \$2 ORDER BY id LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 51).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}).
			AddRow("binding-a", "deployment-a", "asset-a", "companion", "demo", `["exec"]`, "admin-user", nil, time.Now()))
	listed := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/assets/asset-a/bindings", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"subject":{"id":"demo","type":"companion"}`) ||
		!strings.Contains(listed.Body.String(), `"capabilityAllowlist":["exec"]`) {
		t.Fatalf("list bindings = %d %s", listed.Code, listed.Body.String())
	}

	// A binding under the wrong asset is a not-found, never a cross-asset delete.
	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "binding-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}).
			AddRow("binding-a", "deployment-a", "asset-other", "companion", "demo", nil, "admin-user", nil, time.Now()))
	mismatched := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/assets/asset-a/bindings/binding-a", "")
	if mismatched.Code != http.StatusNotFound {
		t.Fatalf("mismatched binding delete = %d %s", mismatched.Code, mismatched.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "binding-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}).
			AddRow("binding-a", "deployment-a", "asset-a", "companion", "demo", nil, "admin-user", nil, time.Now()))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "asset_bindings" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "binding-a").WillReturnResult(sqlmock.NewResult(0, 1))
	expectAuditInsert(mock)
	mock.ExpectCommit()
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/assets/asset-a/bindings/binding-a", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete binding = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestAdminAssetPermissionDenied(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectQuery(`SELECT EXISTS \(`).WithArgs("deployment-a", "user-a", "assets.read").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	response := userRequest(New(application).Handler(), userToken, http.MethodGet, "/aep/v1/admin/assets", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ACCESS_DENIED") {
		t.Fatalf("non-admin asset list = %d %s", response.Code, response.Body.String())
	}
}
