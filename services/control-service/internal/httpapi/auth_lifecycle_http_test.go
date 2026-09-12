package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
)

func configureAuthHTTPApplication(application *app.App) {
	application.Config.Environment = "test"
	application.Config.DeploymentName = "Deployment A"
	application.Config.BootstrapDeploymentName = "Deployment A"
	application.Config.BootstrapAdminUsername = "admin"
	application.Config.Issuer = "https://issuer.example"
	application.Config.AccessTTL = time.Minute
	application.Config.ModelAccessTTL = 2 * time.Minute
	application.Config.RefreshTTL = 24 * time.Hour
	application.Config.LoginFailureLimit = 5
	application.Config.LoginSourceFailureLimit = 100
	application.Config.LoginFailureWindow = 15 * time.Minute
	application.Config.LoginBackoffBase = 30 * time.Second
	application.Config.LoginBackoffMax = 15 * time.Minute
}

func issueHTTPUserToken(t *testing.T, application *app.App) string {
	t.Helper()
	token, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-user", false, false, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func expectNoLoginThrottle(pool pgxmock.PgxPoolIface) {
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
}

func expectHTTPModelScopes(pool pgxmock.PgxPoolIface, deploymentID, userID string, modelIDs ...string) {
	rows := pgxmock.NewRows([]string{"id"})
	for _, modelID := range modelIDs {
		rows.AddRow(modelID)
	}
	pool.ExpectQuery(`SELECT DISTINCT m\.id`).WithArgs(deploymentID, userID).WillReturnRows(rows)
}

func expectHTTPUserRoles(mock sqlmock.Sqlmock, deploymentID, userID string, roleIDs ...string) {
	rows := sqlmock.NewRows([]string{"role_id"})
	for _, roleID := range roleIDs {
		rows.AddRow(roleID)
	}
	mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id = \$2 ORDER BY role_id`).
		WithArgs(deploymentID, userID).WillReturnRows(rows)
}

func expectHTTPSessionIssue(pool pgxmock.PgxPoolIface, deploymentID, userID string) {
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO user_sessions`).WithArgs(pgxmock.AnyArg(), deploymentID, userID, "user:"+deploymentID+":"+userID).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectExec(`INSERT INTO user_session_tokens`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(pgxmock.AnyArg(), deploymentID, userID).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
	pool.ExpectCommit()
}

func expectLoginSuccessAudit(pool pgxmock.PgxPoolIface, deploymentID, userID string) {
	pool.ExpectBegin()
	pool.ExpectExec(`DELETE FROM login_rate_limits`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("DELETE 1"))
	pool.ExpectExec(`INSERT INTO authentication_audit_events`).WithArgs(
		deploymentID, userID, "login.succeeded", "success", nil, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectCommit()
}

func TestPasswordLoginSuccessAndAudit(t *testing.T) {
	application, mock, pool, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	handler := New(application).Handler()
	password := "correct-password-123"
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	expectNoLoginThrottle(pool)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).WithArgs("deployment-a", "alice", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", passwordHash, "active", false, false, now, now))
	expectHTTPModelScopes(pool, "deployment-a", "user-a", "chat-a")
	expectHTTPUserRoles(mock, "deployment-a", "user-a", "member")
	expectHTTPSessionIssue(pool, "deployment-a", "user-a")
	expectLoginSuccessAudit(pool, "deployment-a", "user-a")

	response := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/password/login", `{"deploymentId":"deployment-a","username":"alice","password":"correct-password-123"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("password login = %d %s", response.Code, response.Body.String())
	}
	var tokens app.TokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ModelAccessToken == "" || tokens.SessionID == "" || tokens.DeploymentID != "deployment-a" {
		t.Fatalf("password login tokens = %#v", tokens)
	}
	claims, err := application.Tokens.ParseAccess(tokens.AccessToken)
	if err != nil || claims.Subject != "user-a" || claims.SessionID != tokens.SessionID || len(claims.Roles) != 1 || claims.Roles[0] != "member" {
		t.Fatalf("password login claims = %#v, %v", claims, err)
	}
}

func TestPasswordLoginFailureAndActiveThrottle(t *testing.T) {
	application, mock, pool, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	handler := New(application).Handler()

	expectNoLoginThrottle(pool)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).WithArgs("deployment-a", "missing", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()))
	pool.ExpectBegin()
	pool.ExpectQuery(`INSERT INTO login_rate_limits`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"failure_count"}).AddRow(1))
	pool.ExpectExec(`UPDATE login_rate_limits SET blocked_until`).WithArgs(pgxmock.AnyArg(), nil).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectQuery(`INSERT INTO login_rate_limits`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"failure_count"}).AddRow(1))
	pool.ExpectExec(`UPDATE login_rate_limits SET blocked_until`).WithArgs(pgxmock.AnyArg(), nil).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectExec(`DELETE FROM login_rate_limits WHERE updated_at`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
	pool.ExpectExec(`INSERT INTO authentication_audit_events`).WithArgs(
		"deployment-a", nil, "login.failed", "failure", "invalid_credentials", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectCommit()
	failed := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/password/login", `{"deploymentId":"deployment-a","username":"missing","password":"wrong-password"}`)
	if failed.Code != http.StatusUnauthorized || !strings.Contains(failed.Body.String(), `"code":"INVALID_CREDENTIALS"`) {
		t.Fatalf("failed password login = %d %s", failed.Code, failed.Body.String())
	}

	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"blocked_until"}).AddRow(time.Now().Add(time.Minute)))
	pool.ExpectQuery(`SELECT blocked_until FROM login_rate_limits`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	pool.ExpectExec(`INSERT INTO authentication_audit_events`).WithArgs(
		"deployment-a", nil, "login.throttled", "denied", "backoff_active", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	throttled := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/password/login", `{"deploymentId":"deployment-a","username":"alice","password":"ignored"}`)
	if throttled.Code != http.StatusTooManyRequests || throttled.Header().Get("Retry-After") == "" || !strings.Contains(throttled.Body.String(), `"code":"LOGIN_RATE_LIMITED"`) {
		t.Fatalf("throttled password login = %d %s", throttled.Code, throttled.Body.String())
	}
}

func TestRefreshAndLogoutHTTPLifecycle(t *testing.T) {
	application, mock, pool, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	userToken := issueHTTPUserToken(t, application)
	handler := New(application).Handler()
	rawRefresh := "old-refresh-token"

	pool.ExpectBeginTx(pgx.TxOptions{})
	pool.ExpectQuery(`SELECT t.session_id,s.user_id`).WithArgs(auth.HashRefreshToken(rawRefresh)).WillReturnRows(
		pgxmock.NewRows([]string{"session_id", "user_id", "user_deployment_id", "deployment_id", "expires_at", "revoked_at", "session_revoked_at", "status", "require_password_change", "is_admin"}).
			AddRow("session-user", "user-a", "deployment-a", "deployment-a", time.Now().Add(time.Hour), nil, nil, "active", false, false),
	)
	expectHTTPModelScopes(pool, "deployment-a", "user-a", "chat-a")
	expectHTTPUserRoles(mock, "deployment-a", "user-a", "member")
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs(auth.HashRefreshToken(rawRefresh)).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectExec(`INSERT INTO user_session_tokens`).WithArgs(pgxmock.AnyArg(), "session-user", pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectExec(`UPDATE user_sessions SET last_seen_at=now\(\)`).WithArgs("session-user").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectCommit()
	refreshed := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/refresh", `{"refreshToken":"old-refresh-token","sessionId":"session-user"}`)
	if refreshed.Code != http.StatusOK || !strings.Contains(refreshed.Body.String(), `"sessionId":"session-user"`) || strings.Contains(refreshed.Body.String(), rawRefresh) {
		t.Fatalf("refresh session = %d %s", refreshed.Code, refreshed.Body.String())
	}

	pool.ExpectBeginTx(pgx.TxOptions{})
	pool.ExpectQuery(`SELECT t.session_id,s.user_id`).WithArgs(auth.HashRefreshToken("invalid-refresh")).WillReturnError(pgx.ErrNoRows)
	pool.ExpectRollback()
	invalid := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/refresh", `{"refreshToken":"invalid-refresh","sessionId":"session-user"}`)
	if invalid.Code != http.StatusUnauthorized || !strings.Contains(invalid.Body.String(), `"code":"REFRESH_TOKEN_INVALID"`) {
		t.Fatalf("invalid refresh = %d %s", invalid.Code, invalid.Body.String())
	}

	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs(auth.HashRefreshToken("logout-refresh"), "session-user").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectExec(`UPDATE user_sessions SET revoked_at=now\(\)`).WithArgs("session-user").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	logout := userRequest(handler, userToken, http.MethodPost, "/aep/v1/auth/logout", `{"refreshToken":"logout-refresh"}`)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", logout.Code, logout.Body.String())
	}
}

func TestChangePasswordAndCurrentIdentity(t *testing.T) {
	application, mock, pool, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	userToken := issueHTTPUserToken(t, application)
	handler := New(application).Handler()
	currentPassword := "current-password-123"
	passwordHash, err := auth.HashPassword(currentPassword)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", passwordHash, "active", true, false, now, now))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs("user-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 2"))
	pool.ExpectExec(`UPDATE user_sessions SET revoked_at=now\(\)`).WithArgs("user-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	expectHTTPModelScopes(pool, "deployment-a", "user-a", "chat-a")
	expectHTTPUserRoles(mock, "deployment-a", "user-a", "member")
	expectHTTPSessionIssue(pool, "deployment-a", "user-a")
	pool.ExpectExec(`INSERT INTO authentication_audit_events`).WithArgs(
		"deployment-a", "user-a", "password.changed", "success", nil, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	changed := userRequest(handler, userToken, http.MethodPost, "/aep/v1/auth/password/change", `{"currentPassword":"current-password-123","newPassword":"replacement-password-456"}`)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"passwordChangeRequired":false`) || strings.Contains(changed.Body.String(), "replacement-password-456") {
		t.Fatalf("change password = %d %s", changed.Code, changed.Body.String())
	}

	weak := userRequest(handler, userToken, http.MethodPost, "/aep/v1/auth/password/change", `{"currentPassword":"current-password-123","newPassword":"short"}`)
	if weak.Code != http.StatusBadRequest || !strings.Contains(weak.Body.String(), `"code":"PASSWORD_POLICY_VIOLATION"`) {
		t.Fatalf("weak replacement password = %d %s", weak.Code, weak.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).WithArgs("deployment-a", "user-a", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("user-a", "deployment-a", "alice", "Alice", "alice@example.com", passwordHash, "active", false, false, now, now))
	expectHTTPUserRoles(mock, "deployment-a", "user-a", "member", "operator")
	identity := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/me", "")
	if identity.Code != http.StatusOK || !strings.Contains(identity.Body.String(), `"sessionId":"session-user"`) || !strings.Contains(identity.Body.String(), `"roles":["member","operator"]`) || strings.Contains(identity.Body.String(), passwordHash) {
		t.Fatalf("current identity = %d %s", identity.Code, identity.Body.String())
	}
}

func TestMockFederatedStartAndExchange(t *testing.T) {
	application, mock, pool, _ := newUserHTTPApplication(t)
	configureAuthHTTPApplication(application)
	application.Config.EnableMockFederatedAuth = true
	handler := New(application).Handler()
	now := time.Now().UTC()

	mock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).WithArgs("deployment-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow("deployment-a", "Deployment A", now))
	started := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/federated/start", `{"deploymentId":"deployment-a","methodId":"mock-oidc","redirectUri":"http://localhost/callback","codeChallenge":"challenge","codeChallengeMethod":"S256"}`)
	if started.Code != http.StatusOK {
		t.Fatalf("federated start = %d %s", started.Code, started.Body.String())
	}
	var transaction struct {
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &transaction); err != nil || transaction.TransactionID == "" {
		t.Fatalf("federated transaction = %#v, %v", transaction, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).WithArgs("deployment-a", "admin", 1).
		WillReturnRows(sqlmock.NewRows(userColumns()).AddRow("admin-user", "deployment-a", "admin", "Administrator", nil, "unused", "active", false, true, now, now))
	expectHTTPModelScopes(pool, "deployment-a", "admin-user", "chat-a")
	expectHTTPUserRoles(mock, "deployment-a", "admin-user", "admin")
	expectHTTPSessionIssue(pool, "deployment-a", "admin-user")
	exchanged := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/exchange", `{"transactionId":"`+transaction.TransactionID+`","authorizationCode":"mock-code"}`)
	if exchanged.Code != http.StatusOK || !strings.Contains(exchanged.Body.String(), `"deploymentId":"deployment-a"`) {
		t.Fatalf("federated exchange = %d %s", exchanged.Code, exchanged.Body.String())
	}

	reused := userRequest(handler, "", http.MethodPost, "/aep/v1/auth/exchange", `{"transactionId":"`+transaction.TransactionID+`","authorizationCode":"mock-code"}`)
	if reused.Code != http.StatusUnauthorized || !strings.Contains(reused.Body.String(), `"code":"AUTHORIZATION_CODE_INVALID"`) {
		t.Fatalf("reused federated transaction = %d %s", reused.Code, reused.Body.String())
	}
}
