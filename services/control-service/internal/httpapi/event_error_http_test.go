package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestControlEventRequiresSession(t *testing.T) {
	application, _, _, _ := newRuntimeHTTPApplication(t)
	token, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(application).Handler()
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/aep/v1/user/heartbeat", `{}`},
		{http.MethodGet, "/aep/v1/user/control-events", ""},
		{http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"received"}`},
		{http.MethodPost, "/aep/v1/user/control-events/delivery-1/result", `{"status":"succeeded"}`},
	} {
		response := userRequest(handler, token, test.method, test.path, test.body)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"SESSION_REQUIRED"`) {
			t.Errorf("%s %s = %d %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestHeartbeatAndEventListingMapDatabaseFailures(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM session_control_deliveries`).WithArgs("session-user").WillReturnError(errors.New("heartbeat unavailable"))
	heartbeat := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/heartbeat", `{}`)
	if heartbeat.Code != http.StatusInternalServerError {
		t.Fatalf("heartbeat failure = %d %s", heartbeat.Code, heartbeat.Body.String())
	}

	pool.ExpectExec(`UPDATE session_control_deliveries d SET state='expired'`).WithArgs("session-user").WillReturnError(errors.New("expiry update failed"))
	list := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/control-events?afterCursor=0", "")
	if list.Code != http.StatusInternalServerError {
		t.Fatalf("event expiry failure = %d %s", list.Code, list.Body.String())
	}
}

func TestAcknowledgeDeliveryStateBoundaries(t *testing.T) {
	t.Run("database error", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		pool.ExpectExec(`UPDATE session_control_deliveries SET state='received'`).WithArgs("delivery-1", "session-user", pgxmock.AnyArg()).WillReturnError(errors.New("ack unavailable"))
		response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"received"}`)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("ack database failure = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("missing delivery", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		pool.ExpectExec(`UPDATE session_control_deliveries SET state='received'`).WithArgs("delivery-1", "session-user", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		pool.ExpectQuery(`SELECT state FROM session_control_deliveries`).WithArgs("delivery-1", "session-user").WillReturnError(pgx.ErrNoRows)
		response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"received"}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing delivery = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("state conflict", func(t *testing.T) {
		application, pool, _, userToken := newRuntimeHTTPApplication(t)
		pool.ExpectExec(`UPDATE session_control_deliveries SET state='received'`).WithArgs("delivery-1", "session-user", pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		pool.ExpectQuery(`SELECT state FROM session_control_deliveries`).WithArgs("delivery-1", "session-user").WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("expired"))
		response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"received"}`)
		if response.Code != http.StatusConflict {
			t.Fatalf("ack state conflict = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("invalid status", func(t *testing.T) {
		application, _, _, userToken := newRuntimeHTTPApplication(t)
		response := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/acknowledge", `{"status":"running"}`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid ack status = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestReportDeliveryResultStateBoundaries(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	pool.ExpectExec(`UPDATE session_control_deliveries SET state=`).WithArgs("delivery-1", "session-user", "succeeded", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("result unavailable"))
	failed := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/result", `{"status":"succeeded"}`)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("result database failure = %d %s", failed.Code, failed.Body.String())
	}

	application, pool, _, userToken = newRuntimeHTTPApplication(t)
	pool.ExpectExec(`UPDATE session_control_deliveries SET state=`).WithArgs("delivery-1", "session-user", "succeeded", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	conflict := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/result", `{"status":"succeeded"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("result state conflict = %d %s", conflict.Code, conflict.Body.String())
	}

	application, _, _, userToken = newRuntimeHTTPApplication(t)
	invalid := userRequest(New(application).Handler(), userToken, http.MethodPost, "/aep/v1/user/control-events/delivery-1/result", `{"status":"unknown"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid result status = %d %s", invalid.Code, invalid.Body.String())
	}
}
