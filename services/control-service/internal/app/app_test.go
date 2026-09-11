package app

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

func newMockApplication(t *testing.T) (*App, pgxmock.PgxPoolIface, sqlmock.Sqlmock) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, sqlMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	ormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewService("https://issuer.example", "", time.Minute, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		if err := sqlMock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = sqlDB.Close()
	})
	application := &App{
		Config: config.Config{
			DeploymentID: "deployment-a", BootstrapDeploymentID: "deployment-storage",
			DeploymentName: "Deployment A", BootstrapDeploymentName: "Storage deployment",
			LicenseDeploymentID: "deployment-a", AccessTTL: time.Minute, ModelAccessTTL: 2 * time.Minute,
			RefreshTTL: 24 * time.Hour,
		},
		Store: repository.New(ormDB), Tokens: tokens, runtimeDB: pool,
	}
	return application, pool, sqlMock
}

func testVerifiedLicense() license.Verified {
	expires := "2027-01-01T00:00:00.000Z"
	return license.Verified{
		Envelope: license.Envelope{KeyID: "key-1", Payload: []byte(`{"licenseId":"license-a"}`)},
		Claims: license.Claims{
			LicenseID: "license-a", CustomerID: "customer-a", DeploymentID: "deployment-a",
			IssuedAt: "2026-01-01T00:00:00.000Z", ExpiresAt: &expires, GraceDays: 7,
			Features: []string{"enterprise.models"},
		},
		Digest: "sha256:digest-a", Status: "enterprise-active",
	}
}

func TestDeploymentMetadataAndLicenseSnapshot(t *testing.T) {
	application := &App{Config: config.Config{
		DeploymentID: "deployment-a", DeploymentName: "Deployment A",
		BootstrapDeploymentID: "legacy", BootstrapDeploymentName: "Legacy",
	}}
	if application.DeploymentID() != "deployment-a" || application.DeploymentName() != "Deployment A" {
		t.Fatal("deployment metadata did not prefer the public deployment identity")
	}
	fallback := &App{Config: config.Config{BootstrapDeploymentID: "legacy", BootstrapDeploymentName: "Legacy"}}
	if fallback.DeploymentID() != "legacy" || fallback.DeploymentName() != "Legacy" {
		t.Fatal("deployment metadata fallback is incorrect")
	}
	if fallback.CurrentLicense() != nil {
		t.Fatal("empty application returned a License")
	}
	verified := testVerifiedLicense()
	fallback.SetLicense(verified)
	snapshot := fallback.CurrentLicense()
	snapshot.Status = "modified"
	if fallback.CurrentLicense().Status != "enterprise-active" {
		t.Fatal("CurrentLicense returned mutable application state")
	}
}

func TestRegisterLicenseIsIdempotentAndRejectsDigestConflict(t *testing.T) {
	verified := testVerifiedLicense()
	for _, test := range []struct {
		name           string
		existingDigest string
		wantErr        error
	}{
		{name: "same digest", existingDigest: verified.Digest},
		{name: "different digest", existingDigest: "sha256:other", wantErr: ErrLicenseConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, pool, _ := newMockApplication(t)
			pool.ExpectQuery(regexp.QuoteMeta(`SELECT digest FROM licenses WHERE license_id=$1`)).
				WithArgs("license-a").
				WillReturnRows(pgxmock.NewRows([]string{"digest"}).AddRow(test.existingDigest))
			err := application.RegisterLicense(context.Background(), verified)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RegisterLicense() error = %v, want %v", err, test.wantErr)
			}
		})
	}

	application, pool, _ := newMockApplication(t)
	application.Config.LicenseDeploymentID = ""
	if err := application.RegisterLicense(context.Background(), verified); err == nil {
		t.Fatal("RegisterLicense accepted a missing deployment binding")
	}
	pool.ExpectQuery(regexp.QuoteMeta(`SELECT digest FROM licenses WHERE license_id=$1`)).
		WithArgs("license-a").WillReturnError(pgx.ErrNoRows)
	pool.ExpectExec(`INSERT INTO licenses`).
		WithArgs("license-a", "customer-a", "deployment-a", "sha256:digest-a", "key-1", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), []string{"enterprise.models"}, verified.Envelope.Payload).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	application.Config.LicenseDeploymentID = "deployment-a"
	if err := application.RegisterLicense(context.Background(), verified); err != nil {
		t.Fatal(err)
	}
}

func TestActivateLicenseLifecycle(t *testing.T) {
	t.Run("not registered", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).
			WithArgs("license-a", "deployment-a").WillReturnError(pgx.ErrNoRows)
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); !errors.Is(err, ErrLicenseNotRegistered) {
			t.Fatalf("ActivateLicense() error = %v", err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).
			WithArgs("license-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"status", "revoked_at"}).AddRow("revoked", nil))
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); !errors.Is(err, ErrLicenseRevoked) {
			t.Fatalf("ActivateLicense() error = %v", err)
		}
	})

	t.Run("refreshes existing activation", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).
			WithArgs("license-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"status", "revoked_at"}).AddRow("active", nil))
		pool.ExpectQuery(`SELECT EXISTS`).
			WithArgs("license-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
		pool.ExpectExec(`UPDATE license_activations SET last_seen_at=now\(\)`).
			WithArgs("license-a", "deployment-a").
			WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
		pool.ExpectCommit()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("creates activation", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).
			WithArgs("license-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"status", "revoked_at"}).AddRow("active", nil))
		pool.ExpectQuery(`SELECT EXISTS`).
			WithArgs("license-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		pool.ExpectExec(`INSERT INTO license_activations`).
			WithArgs(pgxmock.AnyArg(), "license-a", "deployment-a", "user-a").
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		pool.ExpectCommit()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err != nil {
			t.Fatal(err)
		}
	})
}

func expectModelScopes(pool pgxmock.PgxPoolIface, modelIDs ...string) {
	rows := pgxmock.NewRows([]string{"id"})
	for _, modelID := range modelIDs {
		rows.AddRow(modelID)
	}
	pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-storage", "user-a").WillReturnRows(rows)
}

func expectUserRoles(sqlMock sqlmock.Sqlmock, roleIDs ...string) {
	rows := sqlmock.NewRows([]string{"role_id"})
	for _, roleID := range roleIDs {
		rows.AddRow(roleID)
	}
	sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id = \$2 ORDER BY role_id`).
		WithArgs("deployment-storage", "user-a").WillReturnRows(rows)
}

func TestModelScopesAndIssueUserSession(t *testing.T) {
	application, pool, sqlMock := newMockApplication(t)
	expectModelScopes(pool, "chat-a", "chat-b")
	expectUserRoles(sqlMock, "member", "operator")
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO user_sessions`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a", "user:deployment-a:user-a").
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectExec(`INSERT INTO user_session_tokens`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectExec(`INSERT INTO session_control_deliveries`).
		WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a").
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 2"))
	pool.ExpectCommit()

	result, err := application.IssueUserSession(context.Background(), repository.User{
		ID: "user-a", DeploymentID: "deployment-storage", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" || result.RefreshToken == "" || result.DeploymentID != "deployment-a" || result.ExpiresIn != 60 || result.ModelAccessExpiresIn != 120 {
		t.Fatalf("IssueUserSession() = %#v", result)
	}
	accessClaims, err := application.Tokens.ParseAccess(result.AccessToken)
	if err != nil || accessClaims.Subject != "user-a" || len(accessClaims.Roles) != 2 || accessClaims.SessionID != result.SessionID {
		t.Fatalf("access claims = %#v, %v", accessClaims, err)
	}
	modelClaims, err := application.Tokens.ParseModel(result.ModelAccessToken)
	if err != nil || len(modelClaims.ModelScopes) != 2 || modelClaims.ModelScopes[0] != "chat-a" {
		t.Fatalf("model claims = %#v, %v", modelClaims, err)
	}
}

func TestRefreshUserSessionRotatesTokenAndPreservesSession(t *testing.T) {
	application, pool, sqlMock := newMockApplication(t)
	rawRefresh := "old-refresh-token"
	pool.ExpectBeginTx(pgx.TxOptions{})
	pool.ExpectQuery(`SELECT t.session_id,s.user_id`).
		WithArgs(auth.HashRefreshToken(rawRefresh)).
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "user_id", "user_deployment_id", "deployment_id", "expires_at",
			"revoked_at", "session_revoked_at", "status", "require_password_change", "is_admin",
		}).AddRow("session-a", "user-a", "deployment-storage", "deployment-a", time.Now().Add(time.Hour), nil, nil, "active", false, false))
	expectModelScopes(pool, "chat-a")
	expectUserRoles(sqlMock, "member")
	pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).
		WithArgs(auth.HashRefreshToken(rawRefresh)).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectExec(`INSERT INTO user_session_tokens`).
		WithArgs(pgxmock.AnyArg(), "session-a", pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	pool.ExpectExec(`UPDATE user_sessions SET last_seen_at=now\(\)`).
		WithArgs("session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectCommit()

	result, err := application.RefreshUserSession(context.Background(), rawRefresh, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.RefreshToken == "" || result.RefreshToken == rawRefresh || result.SessionID != "session-a" {
		t.Fatalf("RefreshUserSession() = %#v", result)
	}
	claims, err := application.Tokens.ParseModel(result.ModelAccessToken)
	if err != nil || len(claims.ModelScopes) != 1 || claims.ModelScopes[0] != "chat-a" {
		t.Fatalf("model claims = %#v, %v", claims, err)
	}
}

func TestRefreshUserSessionRejectsSessionMismatch(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	rawRefresh := "old-refresh-token"
	pool.ExpectBeginTx(pgx.TxOptions{})
	pool.ExpectQuery(`SELECT t.session_id,s.user_id`).
		WithArgs(auth.HashRefreshToken(rawRefresh)).
		WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "user_id", "user_deployment_id", "deployment_id", "expires_at",
			"revoked_at", "session_revoked_at", "status", "require_password_change", "is_admin",
		}).AddRow("session-a", "user-a", "deployment-storage", "deployment-a", time.Now().Add(time.Hour), nil, nil, "active", false, false))
	pool.ExpectRollback()
	if _, err := application.RefreshUserSession(context.Background(), rawRefresh, "session-other"); !errors.Is(err, ErrRefreshTokenInvalid) {
		t.Fatalf("RefreshUserSession() error = %v", err)
	}
}

func TestSessionRevocationContracts(t *testing.T) {
	application, pool, _ := newMockApplication(t)
	if err := application.RevokeUserSession(context.Background(), "refresh", ""); err != nil {
		t.Fatal(err)
	}
	pool.ExpectExec(`UPDATE user_session_tokens`).
		WithArgs(auth.HashRefreshToken("refresh"), "session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectExec(`UPDATE user_sessions`).
		WithArgs("session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	if err := application.RevokeUserSession(context.Background(), "refresh", "session-a"); err != nil {
		t.Fatal(err)
	}

	if err := application.RevokeUserSessionByID(context.Background(), "", "session-a"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("RevokeUserSessionByID() error = %v", err)
	}
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "missing").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectRollback()
	if err := application.RevokeUserSessionByID(context.Background(), "deployment-a", "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("RevokeUserSessionByID() error = %v", err)
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "session-a").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs("session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectExec(`UPDATE user_sessions`).WithArgs("deployment-a", "session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	pool.ExpectCommit()
	if err := application.RevokeUserSessionByID(context.Background(), "deployment-a", "session-a"); err != nil {
		t.Fatal(err)
	}

	pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs("user-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 2"))
	pool.ExpectExec(`UPDATE user_sessions`).WithArgs("user-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 2"))
	if err := application.RevokeUserSessionSet(context.Background(), "user-a"); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseUnavailableContracts(t *testing.T) {
	application := &App{}
	if err := application.RegisterLicense(context.Background(), testVerifiedLicense()); err != nil {
		t.Fatal(err)
	}
	if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ModelScopes(context.Background(), "deployment-a", "user-a"); err == nil {
		t.Fatal("ModelScopes accepted an unavailable database")
	}
	if _, err := application.IssueUserSession(context.Background(), repository.User{}); err == nil {
		t.Fatal("IssueUserSession accepted an unavailable database")
	}
	if _, err := application.RefreshUserSession(context.Background(), "refresh", "session-a"); err == nil {
		t.Fatal("RefreshUserSession accepted an unavailable database")
	}
	if err := application.RevokeUserSession(context.Background(), "refresh", "session-a"); err != nil {
		t.Fatal(err)
	}
	if err := application.RevokeUserSessionByID(context.Background(), "deployment-a", "session-a"); err != nil {
		t.Fatal(err)
	}
	if err := application.RevokeUserSessionSet(context.Background(), "user-a"); err != nil {
		t.Fatal(err)
	}
}
