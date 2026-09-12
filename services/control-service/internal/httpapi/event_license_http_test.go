package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
)

func newRuntimeHTTPApplication(t *testing.T) (*app.App, pgxmock.PgxPoolIface, string, string) {
	t.Helper()
	application, adminToken, userToken := testHTTPApplication(t)
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	application.SetRuntimeDatabase(pool)
	t.Cleanup(func() {
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		pool.Close()
	})
	return application, pool, adminToken, userToken
}

func userRequest(handler http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestHeartbeatAndControlDeliveryLifecycle(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM session_control_deliveries`).WithArgs("session-user").
		WillReturnRows(pgxmock.NewRows([]string{"exists", "max"}).AddRow(true, nil))
	pool.ExpectExec(`UPDATE session_control_deliveries d SET state='expired'`).WithArgs("session-user").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	heartbeat := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/heartbeat", `{}`)
	if heartbeat.Code != http.StatusOK || !strings.Contains(heartbeat.Body.String(), `"pending":true`) {
		t.Fatalf("heartbeat = %d %s", heartbeat.Code, heartbeat.Body.String())
	}

	pool.ExpectExec(`UPDATE session_control_deliveries d SET state='expired'`).WithArgs("session-user").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT d.delivery_id,e.event_id,d.cursor,e.type,e.scope_type`).WithArgs("session-user", int64(0), int32(50)).
		WillReturnRows(pgxmock.NewRows([]string{"delivery_id", "event_id", "cursor", "type", "scope_type", "scope_id", "resource_type", "resource_id", "resource_revision", "task_type", "created_at", "expires_at"}).
			AddRow("delivery-1", "event-1", int64(42), "skill.manifest.changed", "user", nil, nil, nil, nil, "skill.reconcile", now, now.Add(time.Hour)))
	events := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/control-events?afterCursor=0&limit=50", "")
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), `"deliveryId":"delivery-1"`) {
		t.Fatalf("events = %d %s", events.Code, events.Body.String())
	}

	pool.ExpectExec(`UPDATE session_control_deliveries SET state='received'`).WithArgs("delivery-1", "session-user", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	ack := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"received","receivedAt":"2026-09-11T10:00:00Z"}`)
	if ack.Code != http.StatusNoContent {
		t.Fatalf("ack = %d %s", ack.Code, ack.Body.String())
	}
	pool.ExpectExec(`UPDATE session_control_deliveries SET state='received'`).WithArgs("delivery-1", "session-user", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectQuery(`SELECT state FROM session_control_deliveries`).WithArgs("delivery-1", "session-user").WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("succeeded"))
	idempotentAck := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"received"}`)
	if idempotentAck.Code != http.StatusNoContent {
		t.Fatalf("idempotent ack = %d %s", idempotentAck.Code, idempotentAck.Body.String())
	}

	pool.ExpectExec(`UPDATE session_control_deliveries SET state=`).WithArgs("delivery-1", "session-user", "succeeded", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/result", `{"status":"succeeded"}`)
	if result.Code != http.StatusNoContent {
		t.Fatalf("result = %d %s", result.Code, result.Body.String())
	}
}

func TestControlEventValidationAndAdminQueries(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	for _, body := range []string{
		`{"type":"x","scope":{"type":"global"},"task":{"type":"x"},"expiresAt":"2020-01-01T00:00:00Z"}`,
		`{"type":"x","scope":{"type":"agent"},"task":{"type":"x"},"expiresAt":"` + future + `"}`,
	} {
		response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/control-events", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid event = %d %s", response.Code, response.Body.String())
		}
	}

	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO control_events`).WithArgs(
		pgxmock.AnyArg(), "deployment-a", "skill.manifest.changed", "global", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), "skill.reconcile", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT DISTINCT s.session_id`).WithArgs("deployment-a", "global", pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-user"))
	pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "session-user").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	created := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/control-events", `{"type":"skill.manifest.changed","scope":{"type":"global"},"task":{"type":"skill.reconcile"},"expiresAt":"`+future+`"}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"pending":1`) {
		t.Fatalf("created event = %d %s", created.Code, created.Body.String())
	}

	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT e.event_id,e.type,e.scope_type`).WithArgs("deployment-a", "", int32(50)).WillReturnRows(
		pgxmock.NewRows([]string{"event_id", "type", "scope_type", "scope_id", "resource_type", "resource_id", "resource_revision", "task_type", "expires_at", "state", "created_at", "created_by", "pending", "received", "running", "succeeded", "failed", "expired", "superseded"}).
			AddRow("event-1", "skill.manifest.changed", "global", nil, nil, nil, nil, "skill.reconcile", now.Add(time.Hour), "active", now, "admin-user", int64(1), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)))
	adminEvents := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/control-events", "")
	if adminEvents.Code != http.StatusOK || !strings.Contains(adminEvents.Body.String(), `"eventId":"event-1"`) {
		t.Fatalf("admin events = %d %s", adminEvents.Code, adminEvents.Body.String())
	}

	pool.ExpectExec(`UPDATE control_events SET state='cancelled'`).WithArgs("event-1", "deployment-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE session_control_deliveries SET state='superseded'`).WithArgs("event-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectQuery(`SELECT e.event_id,e.type,e.scope_type`).WithArgs("deployment-a", "event-1", int32(50)).WillReturnRows(
		pgxmock.NewRows([]string{"event_id", "type", "scope_type", "scope_id", "resource_type", "resource_id", "resource_revision", "task_type", "expires_at", "state", "created_at", "created_by", "pending", "received", "running", "succeeded", "failed", "expired", "superseded"}).
			AddRow("event-1", "skill.manifest.changed", "global", nil, nil, nil, nil, "skill.reconcile", now.Add(time.Hour), "cancelled", now, "admin-user", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(1)))
	cancelled := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/control-events/event-1/cancel", "")
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"state":"cancelled"`) {
		t.Fatalf("cancelled event = %d %s", cancelled.Code, cancelled.Body.String())
	}

	pool.ExpectExec(`UPDATE session_control_deliveries d SET state='expired'`).WithArgs("event-1", "deployment-a").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectQuery(`SELECT d.delivery_id,d.event_id,d.session_id,d.state`).WithArgs("event-1", "deployment-a", int32(50)).WillReturnRows(
		pgxmock.NewRows([]string{"delivery_id", "event_id", "session_id", "state", "attempt_count", "received_at", "completed_at", "updated_at", "error_code", "message"}).
			AddRow("delivery-1", "event-1", nil, "superseded", 1, nil, nil, now, nil, nil))
	deliveries := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/control-events/event-1/deliveries", "")
	if deliveries.Code != http.StatusOK || !strings.Contains(deliveries.Body.String(), `"deliveryId":"delivery-1"`) {
		t.Fatalf("deliveries = %d %s", deliveries.Code, deliveries.Body.String())
	}
}

func TestTelemetryUploadAndSearch(t *testing.T) {
	application, pool, adminToken, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	payload := `{"events":[{"eventId":"telemetry-1","type":"skill.sync.completed","occurredAt":"2026-09-11T10:00:00Z","data":{"version":"2"}},{"eventId":"telemetry-2","type":"skill.sync.failed","occurredAt":"2026-09-11T10:01:00Z","result":"failure","data":{}}]}`
	pool.ExpectExec(`INSERT INTO telemetry_events`).WithArgs("telemetry-1", "deployment-a", "user-a", "session-user", "skill.sync.completed", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO telemetry_events`).WithArgs("telemetry-2", "deployment-a", "user-a", "session-user", "skill.sync.failed", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("database unavailable"))
	upload := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/events/batch", payload)
	if upload.Code != http.StatusOK || !strings.Contains(upload.Body.String(), `"accepted":["telemetry-1"]`) || !strings.Contains(upload.Body.String(), `"INTERNAL_ERROR"`) {
		t.Fatalf("telemetry upload = %d %s", upload.Code, upload.Body.String())
	}

	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT event_id,user_id,session_id,type,resource_type`).WithArgs("deployment-a", "skill.sync.completed", 51).WillReturnRows(
		pgxmock.NewRows([]string{"event_id", "user_id", "session_id", "type", "resource_type", "resource_id", "result", "payload", "occurred_at", "received_at"}).
			AddRow("telemetry-1", "user-a", nil, "skill.sync.completed", nil, nil, nil, []byte(`{"version":"2"}`), now, now))
	search := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/events?type=skill.sync.completed&limit=50", "")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), `"eventId":"telemetry-1"`) {
		t.Fatalf("telemetry search = %d %s", search.Code, search.Body.String())
	}

	invalid := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/events?result=unknown", "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid telemetry filter = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestLicenseHTTPReadAndInternalStatus(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config = config.Config{DeploymentID: "deployment-a", GatewayLicenseStatusToken: "gateway-secret"}
	handler := New(application).Handler()
	now := time.Now().UTC()
	columns := []string{"license_id", "deployment_id", "customer_id", "digest", "key_id", "status", "issued_at", "expires_at", "grace_ends_at", "features", "payload", "revoked_at", "created_at", "updated_at", "active_activations", "active_users"}
	row := pgxmock.NewRows(columns).AddRow("lic-1", "deployment-a", "customer-1", "sha256:abc", "key-1", "active", now, nil, nil, []string{"enterprise.models"}, []byte(`{"licenseId":"lic-1"}`), nil, now, now, 2, 1)
	pool.ExpectQuery(`SELECT l\.license_id,l\.deployment_id`).WithArgs("deployment-a", int32(50)).WillReturnRows(row)
	list := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/licenses?limit=50", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"licenseId":"lic-1"`) || strings.Contains(list.Body.String(), `"payload"`) {
		t.Fatalf("license list = %d %s", list.Code, list.Body.String())
	}

	pool.ExpectQuery(`SELECT l\.license_id,l\.deployment_id`).WithArgs("deployment-a", "lic-1").WillReturnRows(pgxmock.NewRows(columns).AddRow("lic-1", "deployment-a", "customer-1", "sha256:abc", "key-1", "active", now, nil, nil, []string{"enterprise.models"}, []byte(`{"licenseId":"lic-1"}`), nil, now, now, 0, 0))
	get := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/licenses/lic-1", "")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"payload":{"licenseId":"lic-1"}`) {
		t.Fatalf("license get = %d %s", get.Code, get.Body.String())
	}

	pool.ExpectQuery(`SELECT status,digest,deployment_id FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnRows(pgxmock.NewRows([]string{"status", "digest", "deployment_id"}).AddRow("active", "sha256:abc", "deployment-a"))
	pool.ExpectQuery(`SELECT EXISTS\(`).WithArgs("deployment-a", "session-user", "user-a", "model-a").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	internal := httptest.NewRequest(http.MethodGet, "/internal/gateway/licenses/lic-1", nil)
	internal.Header.Set("X-AEP-Gateway-Token", "gateway-secret")
	internal.Header.Set("X-AEP-Deployment-ID", "deployment-a")
	internal.Header.Set("X-AEP-User-ID", "user-a")
	internal.Header.Set("X-AEP-Session-ID", "session-user")
	internal.Header.Set("X-AEP-Model-ID", "model-a")
	internalResponse := httptest.NewRecorder()
	handler.ServeHTTP(internalResponse, internal)
	if internalResponse.Code != http.StatusOK || !strings.Contains(internalResponse.Body.String(), `"active":true`) {
		t.Fatalf("internal status = %d %s", internalResponse.Code, internalResponse.Body.String())
	}
}

func TestInternalLicenseStatusFailsClosedForInactiveContext(t *testing.T) {
	for _, test := range []struct {
		name          string
		licenseStatus string
		contextActive *bool
	}{
		{name: "revoked License", licenseStatus: "revoked"},
		{name: "inactive session or model assignment", licenseStatus: "active", contextActive: func() *bool { value := false; return &value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, pool, _, _ := newRuntimeHTTPApplication(t)
			handler := New(application).Handler()
			pool.ExpectQuery(`SELECT status,digest,deployment_id FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnRows(
				pgxmock.NewRows([]string{"status", "digest", "deployment_id"}).AddRow(test.licenseStatus, "sha256:abc", "deployment-a"),
			)
			if test.contextActive != nil {
				pool.ExpectQuery(`SELECT EXISTS\(`).WithArgs("deployment-a", "session-user", "user-a", "model-a").WillReturnRows(
					pgxmock.NewRows([]string{"exists"}).AddRow(*test.contextActive),
				)
			}
			request := httptest.NewRequest(http.MethodGet, "/internal/gateway/licenses/lic-1", nil)
			request.Header.Set("X-AEP-Gateway-Token", "gateway-secret")
			request.Header.Set("X-AEP-Deployment-ID", "deployment-a")
			request.Header.Set("X-AEP-User-ID", "user-a")
			request.Header.Set("X-AEP-Session-ID", "session-user")
			request.Header.Set("X-AEP-Model-ID", "model-a")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active":false`) {
				t.Fatalf("internal status = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLicenseImportLifecycle(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config.LicenseDeploymentID = "deployment-a"
	application.Config.LicenseCustomerID = "customer-1"
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	envelope, publicKey := signedLicenseFor(t, expiresAt, "customer-1", "deployment-a")
	verifier, err := license.NewVerifier(map[string]string{"license-prod-1": publicKey}, "deployment-a")
	if err != nil {
		t.Fatal(err)
	}
	application.LicenseVerifier = verifier
	handler := New(application).Handler()

	pool.ExpectQuery(`SELECT digest FROM licenses WHERE license_id=\$1`).
		WithArgs("lic-1").WillReturnError(pgx.ErrNoRows)
	pool.ExpectExec(`INSERT INTO licenses`).
		WithArgs("lic-1", "customer-1", "deployment-a", pgxmock.AnyArg(), "license-prod-1", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), []string{"enterprise.models"}, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO license_audit_events`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "lic-1", "admin-user", "import", "success", (*string)(nil)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	now := time.Now().UTC()
	columns := []string{"license_id", "deployment_id", "customer_id", "digest", "key_id", "status", "issued_at", "expires_at", "grace_ends_at", "features", "payload", "revoked_at", "created_at", "updated_at", "active_activations", "active_users"}
	pool.ExpectQuery(`SELECT l\.license_id,l\.deployment_id`).WithArgs("deployment-a", "lic-1").WillReturnRows(
		pgxmock.NewRows(columns).AddRow("lic-1", "deployment-a", "customer-1", "sha256:digest", "license-prod-1", "active", now, nil, nil, []string{"enterprise.models"}, []byte(`{"licenseId":"lic-1"}`), nil, now, now, 0, 0))

	response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/licenses/import", `{"license":`+string(envelope)+`}`)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"licenseId":"lic-1"`) || strings.Contains(response.Body.String(), `"payload"`) {
		t.Fatalf("license import = %d %s", response.Code, response.Body.String())
	}
}

func TestLicenseImportRejectsCustomerMismatch(t *testing.T) {
	application, _, adminToken, _ := newRuntimeHTTPApplication(t)
	application.Config.LicenseDeploymentID = "deployment-a"
	application.Config.LicenseCustomerID = "customer-other"
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	envelope, publicKey := signedLicenseFor(t, expiresAt, "customer-1", "deployment-a")
	verifier, err := license.NewVerifier(map[string]string{"license-prod-1": publicKey}, "deployment-a")
	if err != nil {
		t.Fatal(err)
	}
	application.LicenseVerifier = verifier
	response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/licenses/import", `{"license":`+string(envelope)+`}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"LICENSE_CUSTOMER_MISMATCH"`) {
		t.Fatalf("customer mismatch = %d %s", response.Code, response.Body.String())
	}
}

func TestLicenseRevokeLifecycle(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT status FROM licenses`).WithArgs("deployment-a", "lic-1").WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	pool.ExpectExec(`UPDATE licenses SET status='revoked'`).WithArgs("deployment-a", "lic-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE license_activations SET revoked_at`).WithArgs("deployment-a", "lic-1").WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	pool.ExpectExec(`INSERT INTO license_audit_events`).WithArgs(pgxmock.AnyArg(), "deployment-a", "lic-1", "admin-user").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/licenses/lic-1/revoke", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT status FROM licenses`).WithArgs("deployment-a", "missing").WillReturnError(pgx.ErrNoRows)
	pool.ExpectRollback()
	notFound := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/licenses/missing/revoke", "")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("revoke missing = %d %s", notFound.Code, notFound.Body.String())
	}
}

func TestLoginThrottleDatabaseBranches(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	server := New(application)
	ctx := context.Background()
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs("key-a").WillReturnError(pgx.ErrNoRows)
	if delay, err := server.loginThrottle(ctx, "key-a", now); err != nil || delay != 0 {
		t.Fatalf("missing throttle = %s, %v", delay, err)
	}
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs("key-b").WillReturnRows(pgxmock.NewRows([]string{"blocked_until"}).AddRow(now.Add(time.Minute)))
	if delay, err := server.loginThrottle(ctx, "key-b", now); err != nil || delay <= 0 {
		t.Fatalf("active throttle = %s, %v", delay, err)
	}
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs("key-c").WillReturnRows(pgxmock.NewRows([]string{"blocked_until"}).AddRow(now.Add(-time.Minute)))
	if delay, err := server.loginThrottle(ctx, "key-c", now); err != nil || delay != 0 {
		t.Fatalf("expired throttle = %s, %v", delay, err)
	}
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs("key-d").WillReturnError(errors.New("database unavailable"))
	if _, err := server.loginThrottle(ctx, "key-d", now); err == nil {
		t.Fatal("database error was swallowed")
	}
}

func TestLoginAuditTransactions(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	application.Config.LoginFailureLimit = 5
	application.Config.LoginFailureWindow = 15 * time.Minute
	application.Config.LoginBackoffBase = 30 * time.Second
	application.Config.LoginBackoffMax = 10 * time.Minute
	server := New(application)
	ctx := context.Background()
	fingerprint := loginFingerprint{KeyHash: "key-hash", PrincipalHash: "principal-hash", SourceHash: "source-hash"}
	now := time.Now().UTC()
	pool.ExpectBegin()
	pool.ExpectQuery(`INSERT INTO login_rate_limits`).WithArgs("key-hash", now, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"failure_count"}).AddRow(1))
	pool.ExpectExec(`UPDATE login_rate_limits SET blocked_until`).WithArgs("key-hash", nil).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`DELETE FROM login_rate_limits`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	pool.ExpectExec(`INSERT INTO authentication_audit_events`).WithArgs("deployment-a", nil, "login.failed", "failure", "invalid_credentials", "principal-hash", "source-hash", now).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	if delay, err := server.recordLoginFailure(ctx, fingerprint, "deployment-a", "", now); err != nil || delay != 0 {
		t.Fatalf("record failure = %s, %v", delay, err)
	}

	pool.ExpectBegin()
	pool.ExpectExec(`DELETE FROM login_rate_limits`).WithArgs("key-hash").WillReturnResult(pgxmock.NewResult("DELETE", 1))
	pool.ExpectExec(`INSERT INTO authentication_audit_events`).WithArgs("deployment-a", "user-a", "login.succeeded", "success", nil, "principal-hash", "source-hash", now).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()
	server.recordLoginSuccess(ctx, fingerprint, "deployment-a", "user-a", now)
}

func TestTelemetryBatchLimitAndLicenseHelperSafety(t *testing.T) {
	application, _, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	events := make([]map[string]any, 101)
	for index := range events {
		events[index] = map[string]any{"eventId": "event-" + strconv.Itoa(index), "type": "x"}
	}
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		t.Fatal(err)
	}
	response := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/user/events/batch", string(body))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "At most 100") {
		t.Fatalf("batch limit = %d %s", response.Code, response.Body.String())
	}
	if got := licenseJSON(licenseRecord{LicenseID: "lic-1", Payload: []byte("not-json")}, true); got["payload"] != nil {
		t.Fatal("invalid license payload was exposed")
	}
}
