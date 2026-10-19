package httpapi

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
)

// sealForTest encrypts a value with the same provider key the shared test
// application configures, so handler tests can decrypt credential rows.
func sealForTest(t *testing.T, deploymentID, credentialID, value string) (ciphertext, nonce, keyID []byte) {
	t.Helper()
	provider, enabled, err := credential.NewProvider(base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901")), "")
	if err != nil || !enabled {
		t.Fatalf("NewProvider() = %v, %v", enabled, err)
	}
	envelope, err := credential.NewSealer(provider).Seal(t.Context(), []byte(value), credential.AssociatedData(deploymentID, credentialID))
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Ciphertext, envelope.Nonce, []byte(envelope.KeyID)
}

func detectableAssetRow(id string, host string, port int, authMethod string) *sqlmock.Rows {
	now := time.Now().UTC()
	spec := fmt.Sprintf(`{"host":%q,"port":%d,"username":"ops","authMethod":%q}`, host, port, authMethod)
	return sqlmock.NewRows(assetHTTPColumns()).
		AddRow("deployment-a", id, "ssh_server", "Bastion", nil, nil, nil, nil,
			`[]`, spec,
			`{"default":"credential-a"}`, `{}`, "active",
			nil, nil, nil, "manual", now, now)
}

func credentialProbeRows(t *testing.T, credentialType string) *sqlmock.Rows {
	ciphertext, nonce, keyID := sealForTest(t, "deployment-a", "credential-a", "probe-secret")
	now := time.Now().UTC()
	return sqlmock.NewRows(credentialHTTPColumns()).
		AddRow("deployment-a", "credential-a", "Probe", "ssh", credentialType, "server_only", ciphertext, nonce, string(keyID), "****cret", true, now, now, now)
}

func TestAdminAssetDetectStoresUnreachable(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	// Bind and close to get a port nothing listens on.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()

	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).
		WillReturnRows(detectableAssetRow("asset-a", "127.0.0.1", address.Port, "password"))
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(credentialProbeRows(t, "password"))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "assets" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).
		WillReturnRows(detectableAssetRowWithHealth("asset-a", "unreachable"))
	expectAuditInsert(mock)
	mock.ExpectCommit()

	response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/detect", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"unreachable"`) {
		t.Fatalf("detect unreachable = %d %s", response.Code, response.Body.String())
	}
}

func detectableAssetRowWithHealth(id, state string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(assetHTTPColumns()).
		AddRow("deployment-a", id, "ssh_server", "Bastion", nil, nil, nil, nil,
			`[]`, `{"host":"127.0.0.1","port":22,"username":"ops","authMethod":"password"}`,
			`{"default":"credential-a"}`, `{}`, "active",
			state, now, "probe stored detail", "manual", now, now)
}

func TestAdminAssetDetectValidation(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	// Credential-requiring probe without a reference is rejected before any write.
	noReference := sqlmock.NewRows(assetHTTPColumns()).
		AddRow("deployment-a", "asset-a", "ssh_server", "Bastion", nil, nil, nil, nil,
			`[]`, `{"host":"127.0.0.1","port":22,"username":"ops","authMethod":"password"}`,
			`{}`, `{}`, "draft", nil, nil, nil, "manual", time.Now(), time.Now())
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(noReference)
	response := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/detect", "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_ASSET") {
		t.Fatalf("detect without credential = %d %s", response.Code, response.Body.String())
	}

	// A type with no registered probe is refused explicitly.
	otherType := sqlmock.NewRows(assetHTTPColumns()).
		AddRow("deployment-a", "asset-b", "printer", "Office Printer", nil, nil, nil, nil,
			`[]`, `{}`, `{}`, `{}`, "draft", nil, nil, nil, "manual", time.Now(), time.Now())
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-b", 1).WillReturnRows(otherType)
	response = adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-b/detect", "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "DETECT_NOT_SUPPORTED") {
		t.Fatalf("detect unsupported type = %d %s", response.Code, response.Body.String())
	}

	// The credential type must match the asset's authMethod.
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).
		WillReturnRows(detectableAssetRow("asset-a", "127.0.0.1", 22, "private_key"))
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "credential-a", 1).
		WillReturnRows(credentialProbeRows(t, "password"))
	response = adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/assets/asset-a/detect", "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_CREDENTIAL_REF") {
		t.Fatalf("detect mismatched credential type = %d %s", response.Code, response.Body.String())
	}
}

// SSH auth material is storable only as server-only credentials.
func TestAdminCredentialSSHTypes(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	clientDelivery := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/credentials",
		`{"name":"Ops Key","service":"ssh","type":"ssh_private_key","deliveryMode":"client","value":"-----BEGIN OPENSSH PRIVATE KEY-----","enabled":true}`)
	if clientDelivery.Code != http.StatusBadRequest {
		t.Fatalf("client-delivery ssh key = %d %s", clientDelivery.Code, clientDelivery.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "credentials"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/credentials",
		`{"name":"Ops Key","service":"ssh","type":"ssh_private_key","deliveryMode":"server_only","value":"-----BEGIN OPENSSH PRIVATE KEY-----","enabled":true}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"type":"ssh_private_key"`) {
		t.Fatalf("server-only ssh key = %d %s", created.Code, created.Body.String())
	}
}
