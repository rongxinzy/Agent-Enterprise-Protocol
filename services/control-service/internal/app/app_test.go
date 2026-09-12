package app

import (
	"context"
	"errors"
	"regexp"
	"strings"
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

func TestRuntimeDatabaseAccessorsAndClose(t *testing.T) {
	application := &App{}
	if application.Database() != nil {
		t.Fatal("Database returned a database for an empty application")
	}
	payload, pool, sqlMock := newMockApplication(t)
	if payload.Database() != pool {
		t.Fatal("Database did not return the configured runtime database")
	}
	var replacement RuntimeDatabase = pool
	application.SetRuntimeDatabase(replacement)
	if application.Database() != replacement {
		t.Fatal("SetRuntimeDatabase did not replace the runtime database")
	}
	if sqlMock == nil {
		t.Fatal("sql mock was not initialized")
	}
	// Close must also be safe for embedded applications that only provide SQLDB.
	payload.Close()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	(&App{SQLDB: db}).Close()
}

func TestOpenRejectsMalformedDatabaseURL(t *testing.T) {
	_, err := Open(context.Background(), config.Config{DatabaseURL: "://malformed"})
	if err == nil {
		t.Fatal("Open accepted a malformed database URL")
	}
}

func TestModelScopesPropagatesQueryAndRowErrors(t *testing.T) {
	t.Run("query error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectQuery(`SELECT DISTINCT m\.id`).WithArgs("deployment-a", "user-a").
			WillReturnError(errors.New("query failed"))
		if _, err := application.ModelScopes(context.Background(), "deployment-a", "user-a"); err == nil {
			t.Fatal("ModelScopes accepted a query failure")
		}
	})
	t.Run("scan error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectQuery(`SELECT DISTINCT m\.id`).WithArgs("deployment-a", "user-a").
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(struct{}{}))
		if _, err := application.ModelScopes(context.Background(), "deployment-a", "user-a"); err == nil {
			t.Fatal("ModelScopes accepted an invalid model id")
		}
	})
	t.Run("rows error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		rows := pgxmock.NewRows([]string{"id"}).AddRow("chat-a").RowError(0, errors.New("stream failed"))
		pool.ExpectQuery(`SELECT DISTINCT m\.id`).WithArgs("deployment-a", "user-a").WillReturnRows(rows)
		if _, err := application.ModelScopes(context.Background(), "deployment-a", "user-a"); err == nil {
			t.Fatal("ModelScopes accepted a row stream failure")
		}
	})
}

func TestRegisterLicensePropagatesDatabaseErrors(t *testing.T) {
	verified := testVerifiedLicense()
	t.Run("lookup error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectQuery(regexp.QuoteMeta(`SELECT digest FROM licenses WHERE license_id=$1`)).
			WithArgs("license-a").WillReturnError(errors.New("lookup failed"))
		if err := application.RegisterLicense(context.Background(), verified); err == nil {
			t.Fatal("RegisterLicense accepted a lookup failure")
		}
	})
	t.Run("insert error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectQuery(regexp.QuoteMeta(`SELECT digest FROM licenses WHERE license_id=$1`)).
			WithArgs("license-a").WillReturnError(pgx.ErrNoRows)
		pool.ExpectExec(`INSERT INTO licenses`).
			WithArgs("license-a", "customer-a", "deployment-a", "sha256:digest-a", "key-1", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), []string{"enterprise.models"}, verified.Envelope.Payload).
			WillReturnError(errors.New("insert failed"))
		if err := application.RegisterLicense(context.Background(), verified); err == nil {
			t.Fatal("RegisterLicense accepted an insert failure")
		}
	})
}

func TestActivateLicensePropagatesTransactionErrors(t *testing.T) {
	setup := func(t *testing.T, status string, revokedAt *time.Time) (*App, pgxmock.PgxPoolIface) {
		t.Helper()
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).
			WithArgs("license-a", "deployment-a").
			WillReturnRows(pgxmock.NewRows([]string{"status", "revoked_at"}).AddRow(status, revokedAt))
		return application, pool
	}
	t.Run("begin error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin failed"))
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err == nil {
			t.Fatal("ActivateLicense accepted a begin failure")
		}
	})
	t.Run("lookup error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).WithArgs("license-a", "deployment-a").WillReturnError(errors.New("lookup failed"))
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err == nil {
			t.Fatal("ActivateLicense accepted a lookup failure")
		}
	})
	t.Run("revoked timestamp", func(t *testing.T) {
		revoked := time.Now()
		application, pool := setup(t, "active", &revoked)
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); !errors.Is(err, ErrLicenseRevoked) {
			t.Fatalf("ActivateLicense() error = %v", err)
		}
	})
	t.Run("activation lookup error", func(t *testing.T) {
		application, pool := setup(t, "active", nil)
		pool.ExpectQuery(`SELECT EXISTS`).WithArgs("license-a", "deployment-a").WillReturnError(errors.New("activation lookup failed"))
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err == nil {
			t.Fatal("ActivateLicense accepted an activation lookup failure")
		}
	})
	t.Run("refresh update error", func(t *testing.T) {
		application, pool := setup(t, "active", nil)
		pool.ExpectQuery(`SELECT EXISTS`).WithArgs("license-a", "deployment-a").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
		pool.ExpectExec(`UPDATE license_activations SET last_seen_at=now\(\)`).WithArgs("license-a", "deployment-a").WillReturnError(errors.New("update failed"))
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err == nil {
			t.Fatal("ActivateLicense accepted an activation update failure")
		}
	})
	t.Run("insert error", func(t *testing.T) {
		application, pool := setup(t, "active", nil)
		pool.ExpectQuery(`SELECT EXISTS`).WithArgs("license-a", "deployment-a").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		pool.ExpectExec(`INSERT INTO license_activations`).WithArgs(pgxmock.AnyArg(), "license-a", "deployment-a", "user-a").WillReturnError(errors.New("insert failed"))
		pool.ExpectRollback()
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err == nil {
			t.Fatal("ActivateLicense accepted an activation insert failure")
		}
	})
	t.Run("commit error", func(t *testing.T) {
		application, pool := setup(t, "active", nil)
		pool.ExpectQuery(`SELECT EXISTS`).WithArgs("license-a", "deployment-a").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		pool.ExpectExec(`INSERT INTO license_activations`).WithArgs(pgxmock.AnyArg(), "license-a", "deployment-a", "user-a").WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		pool.ExpectCommit().WillReturnError(errors.New("commit failed"))
		if err := application.ActivateLicense(context.Background(), "license-a", "deployment-a", "user-a"); err == nil {
			t.Fatal("ActivateLicense accepted a commit failure")
		}
	})
}

func TestIssueUserSessionPasswordGateAndFailures(t *testing.T) {
	t.Run("password change suppresses model scopes", func(t *testing.T) {
		application, pool, sqlMock := newMockApplication(t)
		expectModelScopes(pool)
		expectUserRoles(sqlMock, "member")
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO user_sessions`).WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a", "user:deployment-a:user-a").WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		pool.ExpectExec(`INSERT INTO user_session_tokens`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		pool.ExpectExec(`INSERT INTO session_control_deliveries`).WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a").WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))
		pool.ExpectCommit()
		result, err := application.IssueUserSession(context.Background(), repository.User{ID: "user-a", DeploymentID: "deployment-storage", Status: "active", RequirePasswordChange: true})
		if err != nil || !result.PasswordChangeRequired {
			t.Fatalf("IssueUserSession() = %#v, %v", result, err)
		}
		claims, parseErr := application.Tokens.ParseModel(result.ModelAccessToken)
		if parseErr != nil || len(claims.ModelScopes) != 0 {
			t.Fatalf("password-gated model claims = %#v, %v", claims, parseErr)
		}
	})
	t.Run("model scope error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectQuery(`SELECT DISTINCT m\.id`).WithArgs("deployment-storage", "user-a").WillReturnError(errors.New("scope failed"))
		if _, err := application.IssueUserSession(context.Background(), repository.User{ID: "user-a", DeploymentID: "deployment-storage"}); err == nil {
			t.Fatal("IssueUserSession accepted a model scope failure")
		}
	})
	t.Run("role lookup error", func(t *testing.T) {
		application, pool, sqlMock := newMockApplication(t)
		expectModelScopes(pool)
		sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id = \$2 ORDER BY role_id`).WithArgs("deployment-storage", "user-a").WillReturnError(errors.New("role lookup failed"))
		if _, err := application.IssueUserSession(context.Background(), repository.User{ID: "user-a", DeploymentID: "deployment-storage"}); err == nil {
			t.Fatal("IssueUserSession accepted a role lookup failure")
		}
	})
	t.Run("transaction error", func(t *testing.T) {
		application, pool, sqlMock := newMockApplication(t)
		expectModelScopes(pool)
		expectUserRoles(sqlMock, "member")
		pool.ExpectBegin().WillReturnError(errors.New("begin failed"))
		if _, err := application.IssueUserSession(context.Background(), repository.User{ID: "user-a", DeploymentID: "deployment-storage"}); err == nil {
			t.Fatal("IssueUserSession accepted a transaction failure")
		}
	})
	t.Run("session insert error", func(t *testing.T) {
		application, pool, sqlMock := newMockApplication(t)
		expectModelScopes(pool)
		expectUserRoles(sqlMock, "member")
		pool.ExpectBegin()
		pool.ExpectExec(`INSERT INTO user_sessions`).WithArgs(pgxmock.AnyArg(), "deployment-a", "user-a", "user:deployment-a:user-a").WillReturnError(errors.New("insert failed"))
		pool.ExpectRollback()
		if _, err := application.IssueUserSession(context.Background(), repository.User{ID: "user-a", DeploymentID: "deployment-storage"}); err == nil {
			t.Fatal("IssueUserSession accepted a session insert failure")
		}
	})
}

func TestRefreshUserSessionRejectsInvalidAndPropagatesFailures(t *testing.T) {
	type rowState struct {
		revokedToken, revokedSession *time.Time
		expires                      time.Time
		status                       string
	}
	valid := rowState{expires: time.Now().Add(time.Hour), status: "active"}
	run := func(t *testing.T, state rowState, after func(pgxmock.PgxPoolIface, *App, sqlmock.Sqlmock), wantErr error) {
		t.Helper()
		application, pool, sqlMock := newMockApplication(t)
		raw := "old-refresh-token"
		pool.ExpectBeginTx(pgx.TxOptions{})
		pool.ExpectQuery(`SELECT t\.session_id,s\.user_id`).WithArgs(auth.HashRefreshToken(raw)).WillReturnRows(pgxmock.NewRows([]string{
			"session_id", "user_id", "user_deployment_id", "deployment_id", "expires_at", "revoked_at", "session_revoked_at", "status", "require_password_change", "is_admin",
		}).AddRow("session-a", "user-a", "deployment-storage", "deployment-a", state.expires, state.revokedToken, state.revokedSession, state.status, false, false))
		after(pool, application, sqlMock)
		pool.ExpectRollback()
		if _, err := application.RefreshUserSession(context.Background(), raw, "session-a"); (wantErr == nil && err == nil) || (wantErr != nil && !errors.Is(err, wantErr)) {
			t.Fatalf("RefreshUserSession() error = %v, want %v", err, wantErr)
		}
	}
	t.Run("query error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBeginTx(pgx.TxOptions{})
		pool.ExpectQuery(`SELECT t\.session_id,s\.user_id`).WithArgs(auth.HashRefreshToken("old-refresh-token")).WillReturnError(errors.New("query failed"))
		pool.ExpectRollback()
		if _, err := application.RefreshUserSession(context.Background(), "old-refresh-token", "session-a"); !errors.Is(err, ErrRefreshTokenInvalid) {
			t.Fatalf("RefreshUserSession() error = %v", err)
		}
	})
	for _, test := range []struct {
		name  string
		state rowState
	}{
		{name: "revoked token", state: rowState{expires: time.Now().Add(time.Hour), status: "active", revokedToken: ptrTime(time.Now())}},
		{name: "revoked session", state: rowState{expires: time.Now().Add(time.Hour), status: "active", revokedSession: ptrTime(time.Now())}},
		{name: "expired token", state: rowState{expires: time.Now().Add(-time.Minute), status: "active"}},
		{name: "inactive user", state: rowState{expires: time.Now().Add(time.Hour), status: "disabled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run(t, test.state, func(pgxmock.PgxPoolIface, *App, sqlmock.Sqlmock) {}, ErrRefreshTokenInvalid)
		})
	}
	t.Run("model scope error", func(t *testing.T) {
		run(t, valid, func(pool pgxmock.PgxPoolIface, _ *App, _ sqlmock.Sqlmock) {
			pool.ExpectQuery(`SELECT DISTINCT m\.id`).WithArgs("deployment-storage", "user-a").WillReturnError(errors.New("scope failed"))
		}, nil)
	})
	t.Run("role lookup error", func(t *testing.T) {
		run(t, valid, func(pool pgxmock.PgxPoolIface, _ *App, sqlMock sqlmock.Sqlmock) {
			expectModelScopes(pool, "chat-a")
			sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id = \$2 ORDER BY role_id`).WithArgs("deployment-storage", "user-a").WillReturnError(errors.New("role failed"))
		}, nil)
	})
	for _, test := range []struct {
		name      string
		configure func(pgxmock.PgxPoolIface)
	}{
		{name: "old token update error", configure: func(pool pgxmock.PgxPoolIface) {
			pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs(auth.HashRefreshToken("old-refresh-token")).WillReturnError(errors.New("revoke failed"))
		}},
		{name: "new token insert error", configure: func(pool pgxmock.PgxPoolIface) {
			pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs(auth.HashRefreshToken("old-refresh-token")).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
			pool.ExpectExec(`INSERT INTO user_session_tokens`).WithArgs(pgxmock.AnyArg(), "session-a", pgxmock.AnyArg()).WillReturnError(errors.New("rotate failed"))
		}},
		{name: "last seen update error", configure: func(pool pgxmock.PgxPoolIface) {
			pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs(auth.HashRefreshToken("old-refresh-token")).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
			pool.ExpectExec(`INSERT INTO user_session_tokens`).WithArgs(pgxmock.AnyArg(), "session-a", pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
			pool.ExpectExec(`UPDATE user_sessions SET last_seen_at=now\(\)`).WithArgs("session-a").WillReturnError(errors.New("touch failed"))
		}},
		{name: "commit error", configure: func(pool pgxmock.PgxPoolIface) {
			pool.ExpectExec(`UPDATE user_session_tokens SET revoked_at=now\(\)`).WithArgs(auth.HashRefreshToken("old-refresh-token")).WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
			pool.ExpectExec(`INSERT INTO user_session_tokens`).WithArgs(pgxmock.AnyArg(), "session-a", pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
			pool.ExpectExec(`UPDATE user_sessions SET last_seen_at=now\(\)`).WithArgs("session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
			pool.ExpectCommit().WillReturnError(errors.New("commit failed"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run(t, valid, func(pool pgxmock.PgxPoolIface, _ *App, sqlMock sqlmock.Sqlmock) {
				expectModelScopes(pool, "chat-a")
				expectUserRoles(sqlMock, "member")
				test.configure(pool)
			}, nil)
		})
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestSessionRevocationPropagatesDatabaseErrors(t *testing.T) {
	t.Run("token revoke error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs(auth.HashRefreshToken("refresh"), "session-a").WillReturnError(errors.New("token revoke failed"))
		if err := application.RevokeUserSession(context.Background(), "refresh", "session-a"); err == nil {
			t.Fatal("RevokeUserSession accepted a token update failure")
		}
	})
	t.Run("session revoke error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs(auth.HashRefreshToken("refresh"), "session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
		pool.ExpectExec(`UPDATE user_sessions`).WithArgs("session-a").WillReturnError(errors.New("session revoke failed"))
		if err := application.RevokeUserSession(context.Background(), "refresh", "session-a"); err == nil {
			t.Fatal("RevokeUserSession accepted a session update failure")
		}
	})
	t.Run("by id begin error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin().WillReturnError(errors.New("begin failed"))
		if err := application.RevokeUserSessionByID(context.Background(), "deployment-a", "session-a"); err == nil {
			t.Fatal("RevokeUserSessionByID accepted a begin failure")
		}
	})
	t.Run("by id lookup error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectBegin()
		pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "session-a").WillReturnError(errors.New("lookup failed"))
		pool.ExpectRollback()
		if err := application.RevokeUserSessionByID(context.Background(), "deployment-a", "session-a"); err == nil {
			t.Fatal("RevokeUserSessionByID accepted a lookup failure")
		}
	})
	for _, test := range []struct {
		name      string
		firstErr  bool
		secondErr bool
	}{
		{name: "token update error", firstErr: true},
		{name: "session update error", secondErr: true},
		{name: "commit error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, pool, _ := newMockApplication(t)
			pool.ExpectBegin()
			pool.ExpectQuery(`SELECT EXISTS`).WithArgs("deployment-a", "session-a").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
			if test.firstErr {
				pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs("session-a").WillReturnError(errors.New("token update failed"))
			} else {
				pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs("session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
				if test.secondErr {
					pool.ExpectExec(`UPDATE user_sessions`).WithArgs("deployment-a", "session-a").WillReturnError(errors.New("session update failed"))
				} else {
					pool.ExpectExec(`UPDATE user_sessions`).WithArgs("deployment-a", "session-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
					pool.ExpectCommit().WillReturnError(errors.New("commit failed"))
				}
			}
			pool.ExpectRollback()
			if err := application.RevokeUserSessionByID(context.Background(), "deployment-a", "session-a"); err == nil {
				t.Fatal("RevokeUserSessionByID accepted a transaction failure")
			}
		})
	}
	t.Run("set token update error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs("user-a").WillReturnError(errors.New("token update failed"))
		if err := application.RevokeUserSessionSet(context.Background(), "user-a"); err == nil {
			t.Fatal("RevokeUserSessionSet accepted a token update failure")
		}
	})
	t.Run("set session update error", func(t *testing.T) {
		application, pool, _ := newMockApplication(t)
		pool.ExpectExec(`UPDATE user_session_tokens`).WithArgs("user-a").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
		pool.ExpectExec(`UPDATE user_sessions`).WithArgs("user-a").WillReturnError(errors.New("session update failed"))
		if err := application.RevokeUserSessionSet(context.Background(), "user-a"); err == nil {
			t.Fatal("RevokeUserSessionSet accepted a session update failure")
		}
	})
}

func newBootstrapMock(t *testing.T) (*App, pgxmock.PgxConnIface, sqlmock.Sqlmock) {
	t.Helper()
	application, _, sqlMock := newMockApplication(t)
	application.Config.BootstrapAdminUsername = "admin"
	application.Config.BootstrapAdminDisplayName = "Administrator"
	application.Config.BootstrapAdminPassword = "bootstrap-password"
	connection, err := pgxmock.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return application, connection, sqlMock
}

func expectBootstrapDeployment(sqlMock sqlmock.Sqlmock, id, name string) {
	sqlMock.ExpectExec(`INSERT INTO deployments`).
		WithArgs(id, name).WillReturnResult(sqlmock.NewResult(1, 1))
	sqlMock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).
		WithArgs(id, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow(id, name, time.Now().UTC()))
}

func prepareBootstrapScenario(t *testing.T, connection pgxmock.PgxConnIface, sqlMock sqlmock.Sqlmock, failAt string) {
	t.Helper()
	failure := errors.New(failAt + " failed")
	const lockID int64 = 0x4145505F424F4F54

	lock := connection.ExpectExec(`SELECT pg_advisory_lock\(\$1\)`).WithArgs(lockID)
	if failAt == "lock" {
		lock.WillReturnError(failure)
		return
	}
	lock.WillReturnResult(pgconn.NewCommandTag("SELECT 1"))

	expectUnlock := func() {
		unlock := connection.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).WithArgs(lockID)
		if failAt == "unlock" {
			unlock.WillReturnError(failure)
			connection.ExpectClose()
		} else {
			unlock.WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
		}
	}

	publicDeployment := sqlMock.ExpectExec(`INSERT INTO deployments`).
		WithArgs("deployment-a", "Deployment A")
	if failAt == "public deployment" {
		publicDeployment.WillReturnError(failure)
		expectUnlock()
		return
	}
	publicDeployment.WillReturnResult(sqlmock.NewResult(1, 1))
	sqlMock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).
		WithArgs("deployment-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow("deployment-a", "Deployment A", time.Now().UTC()))

	storageDeployment := sqlMock.ExpectExec(`INSERT INTO deployments`).
		WithArgs("deployment-storage", "Storage deployment")
	if failAt == "storage deployment" {
		storageDeployment.WillReturnError(failure)
		expectUnlock()
		return
	}
	storageDeployment.WillReturnResult(sqlmock.NewResult(1, 1))
	sqlMock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).
		WithArgs("deployment-storage", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow("deployment-storage", "Storage deployment", time.Now().UTC()))

	role := connection.ExpectExec(`INSERT INTO roles`).WithArgs("deployment-storage")
	if failAt == "role" {
		role.WillReturnError(failure)
		expectUnlock()
		return
	}
	role.WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	permissions := connection.ExpectExec(`INSERT INTO role_permissions`).WithArgs("deployment-storage")
	if failAt == "permissions" {
		permissions.WillReturnError(failure)
		expectUnlock()
		return
	}
	permissions.WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	team := connection.ExpectExec(`INSERT INTO teams`).WithArgs("deployment-storage")
	if failAt == "team" {
		team.WillReturnError(failure)
		expectUnlock()
		return
	}
	team.WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	userLookup := sqlMock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).
		WithArgs("deployment-storage", "admin", 1)
	if failAt == "user lookup" {
		userLookup.WillReturnError(failure)
		expectUnlock()
		return
	}
	userLookup.WillReturnRows(sqlmock.NewRows([]string{
		"id", "deployment_id", "username", "display_name", "email", "password_hash",
		"status", "require_password_change", "is_admin", "created_at", "updated_at",
	}).AddRow(
		"admin-user", "deployment-storage", "admin", "Administrator", nil, "hash",
		"active", false, true, time.Now().UTC(), time.Now().UTC(),
	))

	roleBinding := connection.ExpectExec(`INSERT INTO user_role_bindings`).WithArgs("deployment-storage", "admin-user")
	if failAt == "role binding" {
		roleBinding.WillReturnError(failure)
		expectUnlock()
		return
	}
	roleBinding.WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	teamBinding := connection.ExpectExec(`INSERT INTO user_team_bindings`).WithArgs("deployment-storage", "admin-user")
	if failAt == "team binding" {
		teamBinding.WillReturnError(failure)
		expectUnlock()
		return
	}
	teamBinding.WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	expectUnlock()
}

func TestBootstrapWithConnectionBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		failAt        string
		wantErrDetail string
	}{
		{name: "success"},
		{name: "lock failure", failAt: "lock", wantErrDetail: "acquire bootstrap lock: lock failed"},
		{name: "public deployment failure", failAt: "public deployment", wantErrDetail: "bootstrap deployment: public deployment failed"},
		{name: "storage deployment failure", failAt: "storage deployment", wantErrDetail: "storage deployment failed"},
		{name: "role failure", failAt: "role", wantErrDetail: "bootstrap administrator role definition: role failed"},
		{name: "permission failure", failAt: "permissions", wantErrDetail: "bootstrap administrator permissions: permissions failed"},
		{name: "team failure", failAt: "team", wantErrDetail: "bootstrap default team: team failed"},
		{name: "user lookup failure", failAt: "user lookup", wantErrDetail: "user lookup failed"},
		{name: "role binding failure", failAt: "role binding", wantErrDetail: "bootstrap administrator role: role binding failed"},
		{name: "team binding failure", failAt: "team binding", wantErrDetail: "bootstrap administrator team: team binding failed"},
		{name: "unlock failure closes connection", failAt: "unlock"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application, connection, sqlMock := newBootstrapMock(t)
			prepareBootstrapScenario(t, connection, sqlMock, test.failAt)
			err := application.bootstrapWithConnection(context.Background(), connection)
			if test.wantErrDetail == "" {
				if err != nil {
					t.Fatalf("bootstrapWithConnection() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErrDetail) {
				t.Fatalf("bootstrapWithConnection() error = %v, want detail %q", err, test.wantErrDetail)
			}
		})
	}
}

func TestBootstrapCreatesMissingAdministrator(t *testing.T) {
	application, connection, sqlMock := newBootstrapMock(t)
	const lockID int64 = 0x4145505F424F4F54
	connection.ExpectExec(`SELECT pg_advisory_lock\(\$1\)`).WithArgs(lockID).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
	expectBootstrapDeployment(sqlMock, "deployment-a", "Deployment A")
	expectBootstrapDeployment(sqlMock, "deployment-storage", "Storage deployment")
	connection.ExpectExec(`INSERT INTO roles`).WithArgs("deployment-storage").WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	connection.ExpectExec(`INSERT INTO role_permissions`).WithArgs("deployment-storage").WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	connection.ExpectExec(`INSERT INTO teams`).WithArgs("deployment-storage").WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	sqlMock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).
		WithArgs("deployment-storage", "admin", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "deployment_id", "username", "display_name", "email", "password_hash",
			"status", "require_password_change", "is_admin", "created_at", "updated_at",
		}))
	sqlMock.ExpectBegin()
	sqlMock.ExpectExec(`INSERT INTO "users"`).WillReturnResult(sqlmock.NewResult(1, 1))
	sqlMock.ExpectCommit()
	connection.ExpectExec(`INSERT INTO user_role_bindings`).
		WithArgs("deployment-storage", pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	connection.ExpectExec(`INSERT INTO user_team_bindings`).
		WithArgs("deployment-storage", pgxmock.AnyArg()).WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	connection.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).WithArgs(lockID).
		WillReturnResult(pgconn.NewCommandTag("SELECT 1"))

	if err := application.bootstrapWithConnection(context.Background(), connection); err != nil {
		t.Fatalf("bootstrapWithConnection() error = %v", err)
	}
}
