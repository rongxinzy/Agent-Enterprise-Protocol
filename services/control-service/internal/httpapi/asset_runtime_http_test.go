package httpapi

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

// newRuntimeAssetTestApp wires both database surfaces the runtime endpoints
// need: the GORM store on sqlmock and the raw pool on pgxmock (kill switch).
func newRuntimeAssetTestApp(t *testing.T) (*app.App, pgxmock.PgxPoolIface, sqlmock.Sqlmock, string) {
	t.Helper()
	application, adminToken, _ := testHTTPApplication(t)
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	application.SetRuntimeDatabase(pool)

	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = sqlDB.Close()
	})
	ormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	application.Store = repository.New(ormDB)
	return application, pool, mock, adminToken
}

func expectKillSwitchNotHalted(pool pgxmock.PgxPoolIface) {
	pool.ExpectQuery(`SELECT assets_execute_enabled FROM deployment_settings`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"assets_execute_enabled"}).AddRow(nil))
}

func bindingRow(id, assetID, subjectType, subjectID, allowlist string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}).
		AddRow(id, "deployment-a", assetID, subjectType, subjectID, allowlist, "admin-user", nil, now)
}

func TestRuntimeAssetsListForSubject(t *testing.T) {
	application, _, mock, adminToken := newRuntimeAssetTestApp(t)
	handler := New(application).Handler()

	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND subject_type = \$2 AND subject_id = \$3`).
		WithArgs("deployment-a", "de_employee", "net-admin").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}).
			AddRow("binding-a", "deployment-a", "asset-a", "de_employee", "net-admin", `["exec"]`, "admin-user", nil, time.Now()).
			AddRow("binding-b", "deployment-a", "asset-b", "de_employee", "net-admin", nil, "admin-user", nil, time.Now()))
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "active"))
	// asset-b is disabled and drops out of the runtime view.
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-b", 1).WillReturnRows(assetHTTPRows("asset-b", "Old Bastion", "disabled"))

	response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/assets/runtime?subjectType=de_employee&subjectId=net-admin", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"assetId":"asset-a"`) ||
		strings.Contains(response.Body.String(), "asset-b") {
		t.Fatalf("runtime asset list = %d %s", response.Code, response.Body.String())
	}
	// The exec-only allowlist narrows the capability view.
	if !strings.Contains(response.Body.String(), `"key":"exec"`) || strings.Contains(response.Body.String(), `"key":"sftp_write"`) {
		t.Fatalf("allowlist narrowing failed: %s", response.Body.String())
	}

	badSubject := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/assets/runtime?subjectType=robot&subjectId=x", "")
	if badSubject.Code != http.StatusBadRequest {
		t.Fatalf("bad subject = %d %s", badSubject.Code, badSubject.Body.String())
	}
}

func TestAssetExecuteValidation(t *testing.T) {
	application, _, _, adminToken := newRuntimeAssetTestApp(t)
	handler := New(application).Handler()

	for _, test := range []struct {
		name string
		body string
		code string
	}{
		{"unsupported capability", `{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"user-a","capability":"sftp_write","command":"cat x"}`, "UNSUPPORTED_CAPABILITY"},
		{"empty command", `{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"user-a","capability":"exec","command":""}`, "INVALID_ASSET_EXECUTION"},
		{"missing requester", `{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"","capability":"exec","command":"uptime"}`, "INVALID_ASSET_EXECUTION"},
		{"timeout too large", `{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"user-a","capability":"exec","command":"uptime","timeoutSeconds":999}`, "INVALID_ASSET_EXECUTION"},
	} {
		response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/execute", test.body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("%s: execute = %d %s, want 400 %s", test.name, response.Code, response.Body.String(), test.code)
		}
	}
}

func TestAssetExecuteGlobalKillSwitch(t *testing.T) {
	application, pool, _, adminToken := newRuntimeAssetTestApp(t)
	handler := New(application).Handler()

	pool.ExpectQuery(`SELECT assets_execute_enabled FROM deployment_settings`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"assets_execute_enabled"}).AddRow(false))
	response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/execute",
		`{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"user-a","capability":"exec","command":"uptime"}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ASSET_EXECUTION_HALTED") {
		t.Fatalf("halted execution = %d %s", response.Code, response.Body.String())
	}
}

func TestAssetExecuteNotActiveOrNotBound(t *testing.T) {
	application, pool, mock, adminToken := newRuntimeAssetTestApp(t)
	handler := New(application).Handler()
	body := `{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"user-a","capability":"exec","command":"uptime"}`

	// Disabled asset: per-instance emergency stop.
	expectKillSwitchNotHalted(pool)
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "disabled"))
	response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/execute", body)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "ASSET_NOT_ACTIVE") {
		t.Fatalf("disabled asset execution = %d %s", response.Code, response.Body.String())
	}

	// Active asset, but the subject holds no binding.
	expectKillSwitchNotHalted(pool)
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "active"))
	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND subject_type = \$2 AND subject_id = \$3`).
		WithArgs("deployment-a", "de_employee", "net-admin").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}))
	response = adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/execute", body)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ASSET_NOT_BOUND") {
		t.Fatalf("unbound execution = %d %s", response.Code, response.Body.String())
	}

	// Binding exists but its allowlist excludes the capability.
	expectKillSwitchNotHalted(pool)
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetHTTPRows("asset-a", "Bastion", "active"))
	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND subject_type = \$2 AND subject_id = \$3`).
		WithArgs("deployment-a", "de_employee", "net-admin").
		WillReturnRows(bindingRow("binding-a", "asset-a", "de_employee", "net-admin", `["sftp_read"]`))
	response = adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/execute", body)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "CAPABILITY_NOT_ALLOWED") {
		t.Fatalf("allowlist-excluded execution = %d %s", response.Code, response.Body.String())
	}
}

// The full execution chain — kill switch, asset, binding, credential
// unseal, SSH attempt, audit — against a closed port reports the
// classified endpoint failure and still writes the audit row.
func TestAssetExecuteEndpointFailureAudited(t *testing.T) {
	application, pool, mock, adminToken := newRuntimeAssetTestApp(t)
	handler := New(application).Handler()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()

	expectKillSwitchNotHalted(pool)
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(detectableAssetRow("asset-a", "127.0.0.1", address.Port, "password"))
	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND subject_type = \$2 AND subject_id = \$3`).
		WithArgs("deployment-a", "de_employee", "net-admin").
		WillReturnRows(bindingRow("binding-a", "asset-a", "de_employee", "net-admin", `["exec"]`))
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(credentialProbeRows(t, "password"))
	mock.ExpectBegin()
	expectAuditInsert(mock)
	mock.ExpectCommit()

	response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/execute",
		`{"subject":{"type":"de_employee","id":"net-admin"},"requesterUserId":"user-a","capability":"exec","command":"uptime","timeoutSeconds":5}`)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "ASSET_ENDPOINT_FAILED") {
		t.Fatalf("endpoint failure = %d %s", response.Code, response.Body.String())
	}
}
