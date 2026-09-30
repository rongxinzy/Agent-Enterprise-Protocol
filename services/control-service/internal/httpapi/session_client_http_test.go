package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
)

func loginRequestWithUserAgent(handler http.Handler, body, userAgent string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/aep/v1/auth/password/login", strings.NewReader(body))
	request.Header.Set("X-AEP-Protocol-Version", supportedProtocolVersion)
	request.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func expectLoginFlow(pool pgxmock.PgxPoolIface, mock sqlmock.Sqlmock, passwordHash string, now time.Time) {
	expectNoLoginThrottle(pool)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).WithArgs("deployment-a", "alice", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", passwordHash, "active", false, false, "human", now, now))
	expectHTTPModelScopes(pool, "deployment-a", "user-a", "chat-a")
	expectHTTPUserRoles(mock, "deployment-a", "user-a", "member")
}

func TestPasswordLoginRecordsExplicitClientIdentity(t *testing.T) {
	application, mock, pool, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	handler := New(application).Handler()
	password := "correct-password-123"
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	expectLoginFlow(pool, mock, passwordHash, now)
	expectHTTPSessionIssueWithClient(pool, "deployment-a", "user-a", "zhiyuan-desktop", "1.4.0", "device-42")
	expectLoginSuccessAudit(pool, "deployment-a", "user-a")

	response := loginRequestWithUserAgent(handler, `{"deploymentId":"deployment-a","username":"alice","password":"correct-password-123","client":{"name":"zhiyuan-desktop","version":"1.4.0","deviceId":"device-42"}}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("password login with client = %d %s", response.Code, response.Body.String())
	}
}

func TestPasswordLoginDerivesClientIdentityFromUserAgent(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		want      any
	}{
		{name: "browser console", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36", want: "browser"},
		{name: "electron shell", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Electron/38.0.0 Safari/537.36", want: "electron"},
		{name: "node fetch", userAgent: "node", want: "node"},
		{name: "undici fetch", userAgent: "undici", want: "node"},
		{name: "curl", userAgent: "curl/8.9.1", want: "curl"},
		{name: "unrecognized stays empty", userAgent: "Apache-HttpClient/4.5.14 (Java/17.0.12)", want: nil},
		{name: "empty stays empty", userAgent: "", want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application, mock, pool, _ := newUserHTTPApplication(t)
			configureAuthHTTPApplication(application)
			handler := New(application).Handler()
			password := "correct-password-123"
			passwordHash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()

			expectLoginFlow(pool, mock, passwordHash, now)
			expectHTTPSessionIssueWithClient(pool, "deployment-a", "user-a", test.want, nil, nil)
			expectLoginSuccessAudit(pool, "deployment-a", "user-a")

			response := loginRequestWithUserAgent(handler, `{"deploymentId":"deployment-a","username":"alice","password":"correct-password-123"}`, test.userAgent)
			if response.Code != http.StatusOK {
				t.Fatalf("password login = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPasswordLoginRejectsInvalidClientIdentity(t *testing.T) {
	application, _, _, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	handler := New(application).Handler()
	tests := []string{
		`{"deploymentId":"deployment-a","username":"alice","password":"correct-password-123","client":{"version":"1.4.0"}}`,
		`{"deploymentId":"deployment-a","username":"alice","password":"correct-password-123","client":{"name":""}}`,
		`{"deploymentId":"deployment-a","username":"alice","password":"correct-password-123","client":{"name":"` + strings.Repeat("a", 65) + `"}}`,
	}
	for _, body := range tests {
		response := loginRequestWithUserAgent(handler, body, "")
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"INVALID_REQUEST"`) {
			t.Fatalf("invalid client login = %d %s", response.Code, response.Body.String())
		}
	}
}

func TestHeartbeatRefreshesClientIdentity(t *testing.T) {
	application, pool, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	pool.ExpectExec(`UPDATE user_sessions SET client_name`).WithArgs("session-user", "zhiyuan-desktop", "2.0.0", nil).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM session_control_deliveries`).WithArgs("session-user").
		WillReturnRows(pgxmock.NewRows([]string{"exists", "max"}).AddRow(false, nil))
	pool.ExpectExec(`UPDATE session_control_deliveries d SET state='expired'`).WithArgs("session-user").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	heartbeat := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/heartbeat", `{"status":"online","client":{"name":"zhiyuan-desktop","version":"2.0.0"}}`)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat with client = %d %s", heartbeat.Code, heartbeat.Body.String())
	}
}

func TestHeartbeatRejectsInvalidClientIdentity(t *testing.T) {
	application, _, _, userToken := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	heartbeat := userRequest(handler, userToken, http.MethodPost, "/aep/v1/user/heartbeat", `{"status":"online","client":{"name":""}}`)
	if heartbeat.Code != http.StatusBadRequest || !strings.Contains(heartbeat.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("heartbeat with invalid client = %d %s", heartbeat.Code, heartbeat.Body.String())
	}
}
