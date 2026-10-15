package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

// countingRuntimeDatabase counts the administrative audit rows the fallback
// path writes. pgxmock reports an unmatched call only to its caller, and the
// audit write is best-effort, so a duplicate row would otherwise be invisible
// to the test.
type countingRuntimeDatabase struct {
	app.RuntimeDatabase
	auditRows int
}

func (database *countingRuntimeDatabase) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO admin_audit_events") {
		database.auditRows++
	}
	return database.RuntimeDatabase.Exec(ctx, sql, args...)
}

// auditFailurePayloadMatcher asserts the wrapped shape of a failure row payload.
type auditFailurePayloadMatcher struct {
	requestID string
	status    float64
	attempted map[string]any
}

func (matcher auditFailurePayloadMatcher) Match(value any) bool {
	raw, ok := value.([]byte)
	if !ok {
		return false
	}
	var decoded struct {
		RequestID string         `json:"requestId"`
		Status    float64        `json:"status"`
		Request   map[string]any `json:"request"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	if decoded.RequestID != matcher.requestID || decoded.Status != matcher.status {
		return false
	}
	if matcher.attempted == nil {
		return decoded.Request == nil
	}
	encoded, err := json.Marshal(decoded.Request)
	if err != nil {
		return false
	}
	expected, err := json.Marshal(matcher.attempted)
	if err != nil {
		return false
	}
	return string(encoded) == string(expected)
}

// TestAuditAdminWritesRecordsRejectedWrite is the P2-2 regression: a model
// creation rejected with 400 must leave one failure row naming the stable
// problem code, the actor, the target and the request that carried it.
func TestAuditAdminWritesRecordsRejectedWrite(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "create", "model", nil, "INVALID_MODEL",
			auditFailurePayloadMatcher{requestID: "audit-request-1", status: 400, attempted: map[string]any{"displayName": "Rejected Model", "enabled": true}}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	request := httptest.NewRequest(http.MethodPost, "/aep/v1/admin/models", strings.NewReader(`{"displayName":"Rejected Model","enabled":true}`))
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Authorization", "Bearer "+adminToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "audit-request-1")
	response := httptest.NewRecorder()
	New(application).Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("rejected model write = %d %s", response.Code, response.Body.String())
	}
	if counter.auditRows != 1 {
		t.Fatalf("failure audit rows = %d, want exactly 1", counter.auditRows)
	}
}

// TestAuditAdminWritesFailureRowMasksAttemptedSecrets proves the failure path
// goes through the same fail-closed allowlist as a successful write: a rejected
// body must not smuggle a secret into the audit trail.
func TestAuditAdminWritesFailureRowMasksAttemptedSecrets(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "create", "model", nil, "INVALID_REQUEST",
			auditFailurePayloadMatcher{requestID: "audit-request-2", status: 400, attempted: map[string]any{"displayName": "Rejected Model", "password": "[redacted]"}}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	request := httptest.NewRequest(http.MethodPost, "/aep/v1/admin/models", strings.NewReader(`{"displayName":"Rejected Model","password":"hunter2"}`))
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Authorization", "Bearer "+adminToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "audit-request-2")
	response := httptest.NewRecorder()
	New(application).Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("rejected model write = %d %s", response.Code, response.Body.String())
	}
	if counter.auditRows != 1 {
		t.Fatalf("failure audit rows = %d, want exactly 1", counter.auditRows)
	}
}

// TestAuditAdminWritesRecordsPermissionDenial covers the refusal the report
// called out as outside the audit chain: the permission check runs before the
// handler, so the middleware order, not the handler, has to capture it.
func TestAuditAdminWritesRecordsPermissionDenial(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-user", "deployment-a", "session-delegated", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.ExpectQuery(`SELECT EXISTS \(`).
		WithArgs("deployment-a", "delegated-user", "models.write").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "delegated-user", "create", "model", nil, "ACCESS_DENIED", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	response := userRequest(New(application).Handler(), token, http.MethodPost, "/aep/v1/admin/models", `{"displayName":"Denied Model","sourceType":"gateway","protocol":"openai-compatible","endpoint":"https://model.example.test/v1","upstreamModel":"model","capabilities":["text"],"isDefault":false,"enabled":true}`)

	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("denied model write = %d %s", response.Code, response.Body.String())
	}
	if counter.auditRows != 1 {
		t.Fatalf("denial audit rows = %d, want exactly 1", counter.auditRows)
	}
}

// TestAuditAdminWritesFailureAuditDoesNotChangeResponse keeps the failure audit
// best-effort: a broken audit sink must not turn the caller's 400 into a 500.
func TestAuditAdminWritesFailureAuditDoesNotChangeResponse(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("audit sink unavailable"))

	response := userRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/models", `{}`)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_MODEL"`) {
		t.Fatalf("audit failure changed the response: %d %s", response.Code, response.Body.String())
	}
	if counter.auditRows != 1 {
		t.Fatalf("failure audit attempts = %d, want 1", counter.auditRows)
	}
}

// TestAuditAdminWritesDoesNotDuplicateRecordedSuccess is the dedupe half of the
// regression: a handler that recorded its own transaction audit must not get a
// second fallback row.
func TestAuditAdminWritesDoesNotDuplicateRecordedSuccess(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	now := time.Now().UTC()
	pool.ExpectBegin()
	pool.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("deployment-a").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`SELECT configuration FROM gateway_limits`).WithArgs("deployment-a", "rule-a").WillReturnError(pgx.ErrNoRows)
	pool.ExpectQuery(`SELECT count`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	pool.ExpectQuery(`INSERT INTO gateway_limits`).WithArgs("deployment-a", "rule-a", pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"version", "updated_at"}).AddRow(int64(1), now))
	expectAuditTx(pool)
	pool.ExpectCommit()

	response := adminRequest(New(application).Handler(), adminToken, http.MethodPut, "/aep/v1/admin/model-gateway/limits/rule-a", ruleBody)

	if response.Code != http.StatusOK {
		t.Fatalf("create status = %d %s", response.Code, response.Body.String())
	}
	if counter.auditRows != 0 {
		t.Fatalf("fallback audit rows after a recorded success = %d, want 0", counter.auditRows)
	}
}

// TestAuditAdminWritesThrottlesRepeatedIdenticalFailure bounds the capacity
// cost of recording refusals: an identical retry inside the window adds no row,
// while a different attempt still gets its own.
func TestAuditAdminWritesThrottlesRepeatedIdenticalFailure(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	handler := New(application).Handler()
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "create", "model", nil, "INVALID_MODEL", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "create", "model", nil, "INVALID_MODEL", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	rejected := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/models", `{}`)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("rejected model write = %d", rejected.Code)
	}
	repeated := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/models", `{}`)
	if repeated.Code != http.StatusBadRequest {
		t.Fatalf("repeated rejected model write = %d", repeated.Code)
	}
	if counter.auditRows != 1 {
		t.Fatalf("identical repeats wrote %d rows, want 1", counter.auditRows)
	}

	// A different body is a different attempt: it is recorded even inside the
	// window, so throttling never hides distinct failures.
	distinct := userRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/models", `{"displayName":"Only A Name"}`)
	if distinct.Code != http.StatusBadRequest {
		t.Fatalf("distinct rejected model write = %d", distinct.Code)
	}
	if counter.auditRows != 2 {
		t.Fatalf("distinct attempt rows = %d, want 2", counter.auditRows)
	}
}

// TestAuditAdminWritesRecordsOneSuccessRow pins the other half of the
// acceptance criteria: a successful create that relies on the fallback still
// produces exactly one success row, and recording failures did not add a second.
func TestAuditAdminWritesRecordsOneSuccessRow(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	counter := &countingRuntimeDatabase{RuntimeDatabase: pool}
	application.SetRuntimeDatabase(counter)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT count\(\*\) FROM "models"`).WithArgs("deployment-a", "audit-model").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	pool.ExpectBegin()
	pool.ExpectQuery(`INSERT INTO models`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(modelRuntimeColumns()).AddRow("audit-model", "Audit Model", "gateway", "openai-compatible", "https://model.example.test/v1", "model", nil, nil, []string{"text"}, []byte(`{}`), nil, false, true, "unknown", nil, nil, nil, now, now))
	pool.ExpectCommit()
	pool.ExpectExec(`INSERT INTO admin_audit_events`).
		WithArgs("deployment-a", "admin-user", "create", "model", nil, nil, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	response := adminRequest(New(application).Handler(), adminToken, http.MethodPost, "/aep/v1/admin/models", `{"displayName":"Audit Model","sourceType":"gateway","protocol":"openai-compatible","endpoint":"https://model.example.test/v1","upstreamModel":"model","capabilities":["text"],"isDefault":false,"enabled":true}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d %s", response.Code, response.Body.String())
	}
	if counter.auditRows != 1 {
		t.Fatalf("success audit rows = %d, want exactly 1", counter.auditRows)
	}
}

func TestAuditFailureLimiterWindowAndSaturation(t *testing.T) {
	limiter := newAuditFailureLimiter(30 * time.Second)
	now := time.Now()
	if !limiter.allow("a", now) {
		t.Fatal("first attempt of a fingerprint must be admitted")
	}
	if limiter.allow("a", now.Add(time.Second)) {
		t.Fatal("a repeat inside the window must be suppressed")
	}
	if !limiter.allow("a", now.Add(31*time.Second)) {
		t.Fatal("a repeat after the window must be admitted")
	}
	if !limiter.allow("b", now) {
		t.Fatal("a different fingerprint must be admitted")
	}

	// Saturated memory must drop throttling, never evidence.
	limiter = newAuditFailureLimiter(30 * time.Second)
	limiter.maxKeys = 1
	if !limiter.allow("first", now) {
		t.Fatal("first fingerprint must be admitted")
	}
	if !limiter.allow("second", now) {
		t.Fatal("an attempt must be admitted when the limiter is saturated")
	}

	// A disabled limiter (and a nil one, as constructed servers may hold)
	// admits everything.
	if !newAuditFailureLimiter(0).allow("a", now) || !(*auditFailureLimiter)(nil).allow("a", now) {
		t.Fatal("a disabled limiter must admit every attempt")
	}
}
