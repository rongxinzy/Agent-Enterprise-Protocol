package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestAdminControlEventGetAndNotFound(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()
	columns := []string{"event_id", "type", "scope_type", "scope_id", "resource_type", "resource_id", "resource_revision", "task_type", "expires_at", "state", "created_at", "created_by", "pending", "received", "running", "succeeded", "failed", "expired", "superseded"}

	pool.ExpectQuery(`SELECT e.event_id,e.type,e.scope_type`).
		WithArgs("deployment-a", "event-42", int32(50)).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(
			"event-42", "skill.manifest.changed", "global", nil, nil, nil, nil, "skill.reconcile",
			now.Add(time.Hour), "succeeded", now, "admin-user", int64(0), int64(0), int64(0), int64(1), int64(0), int64(0), int64(0),
		))
	got := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/control-events/event-42", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"eventId":"event-42"`) || !strings.Contains(got.Body.String(), `"state":"succeeded"`) || !strings.Contains(got.Body.String(), `"pending":0`) || !strings.Contains(got.Body.String(), `"succeeded":1`) {
		t.Fatalf("get control event = %d %s", got.Code, got.Body.String())
	}

	pool.ExpectQuery(`SELECT e.event_id,e.type,e.scope_type`).
		WithArgs("deployment-a", "missing-event", int32(50)).
		WillReturnRows(pgxmock.NewRows(columns))
	missing := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/control-events/missing-event", "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing control event = %d %s", missing.Code, missing.Body.String())
	}
}

func TestControlEventDeliveriesRoute(t *testing.T) {
	application, pool, adminToken, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	now := time.Now().UTC()

	// The route sweeps expired deliveries before listing them.
	pool.ExpectExec(`UPDATE session_control_deliveries d SET state='expired'`).
		WithArgs("evt-1", "deployment-a").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectQuery(`SELECT d.delivery_id,d.event_id,d.session_id`).
		WithArgs("evt-1", "deployment-a", int32(50)).
		WillReturnRows(pgxmock.NewRows([]string{"delivery_id", "event_id", "session_id", "state", "attempt_count", "received_at", "completed_at", "updated_at", "error_code", "message"}).
			AddRow("delivery-1", "evt-1", "session-user", "pending", int64(1), nil, nil, now, nil, nil))
	got := userRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/control-events/evt-1/deliveries", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"deliveryId":"delivery-1"`) || !strings.Contains(got.Body.String(), `"state":"pending"`) {
		t.Fatalf("deliveries = %d %s", got.Code, got.Body.String())
	}
}
