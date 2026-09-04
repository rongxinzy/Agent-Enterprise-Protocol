package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/blob"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

var (
	ErrRefreshTokenInvalid  = errors.New("refresh token is invalid or expired")
	ErrLicenseNotRegistered = errors.New("license is not registered for this enterprise")
	ErrLicenseRevoked       = errors.New("license has been revoked")
	ErrLicenseConflict      = errors.New("license ID is already registered with a different digest")
	ErrLicenseAgentLimit    = errors.New("license agent limit exceeded")
	ErrLicenseUserLimit     = errors.New("license user limit exceeded")
)

type App struct {
	Config          config.Config
	Pool            *pgxpool.Pool
	SQLDB           *sql.DB
	Store           *repository.Store
	Blobs           blob.Store
	Tokens          *auth.Service
	Credentials     *credential.Sealer
	LicenseVerifier *license.Verifier
	License         *license.Verified
	licenseMu       sync.RWMutex
}

// DeploymentID is the stable identity of this single-deployment installation.
// During the schema migration the legacy enterprise ID remains the storage
// tenant, but all newly issued protocol metadata and tokens carry this value.
func (a *App) DeploymentID() string {
	if value := a.Config.DeploymentID; value != "" {
		return value
	}
	return a.Config.BootstrapDeploymentID
}

func (a *App) DeploymentName() string {
	if value := a.Config.DeploymentName; value != "" {
		return value
	}
	return a.Config.BootstrapDeploymentName
}

func (a *App) CurrentLicense() *license.Verified {
	a.licenseMu.RLock()
	defer a.licenseMu.RUnlock()
	if a.License == nil {
		return nil
	}
	copy := *a.License
	return &copy
}

func (a *App) SetLicense(value license.Verified) {
	a.licenseMu.Lock()
	defer a.licenseMu.Unlock()
	a.License = &value
}

type TokenResponse struct {
	AccessToken            string `json:"accessToken"`
	RefreshToken           string `json:"refreshToken"`
	ModelAccessToken       string `json:"modelAccessToken"`
	TokenType              string `json:"tokenType"`
	ExpiresIn              int64  `json:"expiresIn"`
	ModelAccessExpiresIn   int64  `json:"modelAccessExpiresIn"`
	DeploymentID           string `json:"deploymentId"`
	SessionID              string `json:"sessionId,omitempty"`
	PasswordChangeRequired bool   `json:"passwordChangeRequired"`
}

func Open(ctx context.Context, cfg config.Config) (*App, error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := database.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	blobs, err := blob.NewMinioStore(ctx, cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, cfg.MinioSecure)
	if err != nil {
		pool.Close()
		return nil, err
	}
	tokens, err := auth.NewService(cfg.Issuer, cfg.SigningKeyBase64, cfg.AccessTTL, cfg.ModelAccessTTL)
	if err != nil {
		pool.Close()
		return nil, err
	}
	provider, credentialsEnabled, err := credential.NewProvider(cfg.CredentialMasterKeyBase64, cfg.CredentialMasterKeyFile)
	if err != nil {
		pool.Close()
		return nil, err
	}
	var credentials *credential.Sealer
	if credentialsEnabled {
		credentials = credential.NewSealer(provider)
	}
	licenseVerifier, err := license.NewVerifier(cfg.LicenseTrustedKeys, cfg.LicenseDeploymentID)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("license verifier: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	ormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		_ = sqlDB.Close()
		pool.Close()
		return nil, fmt.Errorf("open GORM: %w", err)
	}
	application := &App{
		Config: cfg, Pool: pool, SQLDB: sqlDB, Store: repository.New(ormDB),
		Blobs: blobs, Tokens: tokens, Credentials: credentials, LicenseVerifier: licenseVerifier,
	}
	if cfg.LicenseFile != "" {
		licenseBytes, readErr := os.ReadFile(cfg.LicenseFile)
		if readErr != nil {
			_ = sqlDB.Close()
			pool.Close()
			return nil, fmt.Errorf("read AEP_LICENSE_FILE: %w", readErr)
		}
		verified, verifyErr := licenseVerifier.Verify(licenseBytes)
		if verifyErr != nil || verified.Status == "enterprise-expired" || (cfg.LicenseCustomerID != "" && verified.Claims.CustomerID != cfg.LicenseCustomerID) {
			_ = sqlDB.Close()
			pool.Close()
			if verifyErr != nil {
				return nil, fmt.Errorf("verify AEP_LICENSE_FILE: %w", verifyErr)
			}
			return nil, errors.New("AEP_LICENSE_FILE is expired or bound to another customer")
		}
		application.SetLicense(verified)
	}
	if err := application.bootstrap(ctx); err != nil {
		_ = sqlDB.Close()
		pool.Close()
		return nil, err
	}
	if application.License != nil {
		if err := application.RegisterLicense(ctx, *application.License); err != nil {
			_ = sqlDB.Close()
			pool.Close()
			return nil, err
		}
	}
	return application, nil
}

func (a *App) Close() {
	if a.SQLDB != nil {
		_ = a.SQLDB.Close()
	}
	a.Pool.Close()
}

func (a *App) RegisterLicense(ctx context.Context, verified license.Verified) error {
	if a.Pool == nil {
		return nil
	}
	claims := verified.Claims
	if a.Config.LicenseDeploymentID == "" {
		return errors.New("AEP_LICENSE_DEPLOYMENT_ID is required to register a license")
	}
	var existingDigest string
	err := a.Pool.QueryRow(ctx, `SELECT digest FROM licenses WHERE license_id=$1`, claims.LicenseID).Scan(&existingDigest)
	if err == nil {
		if existingDigest != verified.Digest {
			return ErrLicenseConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	issuedAt, _ := time.Parse(time.RFC3339Nano, claims.IssuedAt)
	expiresAt, _ := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	graceEndsAt := expiresAt.Add(time.Duration(claims.GraceDays) * 24 * time.Hour)
	_, err = a.Pool.Exec(ctx, `INSERT INTO licenses (license_id,customer_id,deployment_id,digest,key_id,issued_at,expires_at,grace_ends_at,user_limit,activation_limit,features,payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, claims.LicenseID, claims.CustomerID, claims.DeploymentID, verified.Digest, verified.Envelope.KeyID, issuedAt, expiresAt, graceEndsAt, claims.Limits.Users, claims.Limits.Activations, claims.Features, verified.Envelope.Payload)
	return err
}

func (a *App) ActivateLicense(ctx context.Context, licenseID, deploymentID, userID string) error {
	if a.Pool == nil {
		return nil
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var revokedAt *time.Time
	var activationLimit, userLimit int
	err = tx.QueryRow(ctx, `SELECT status, revoked_at, activation_limit, user_limit FROM licenses WHERE license_id=$1 AND deployment_id=$2 FOR UPDATE`, licenseID, deploymentID).Scan(&status, &revokedAt, &activationLimit, &userLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLicenseNotRegistered
	}
	if err != nil {
		return err
	}
	if status != "active" || revokedAt != nil {
		return ErrLicenseRevoked
	}
	var alreadyActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM license_activations WHERE license_id=$1 AND deployment_id=$2 AND revoked_at IS NULL)`, licenseID, deploymentID).Scan(&alreadyActive); err != nil {
		return err
	}
	if alreadyActive {
		_, err = tx.Exec(ctx, `UPDATE license_activations SET last_seen_at=now() WHERE license_id=$1 AND deployment_id=$2 AND revoked_at IS NULL`, licenseID, deploymentID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var activationCount, userCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM license_activations WHERE license_id=$1 AND revoked_at IS NULL`, licenseID).Scan(&activationCount); err != nil {
		return err
	}
	if activationCount >= activationLimit {
		return ErrLicenseAgentLimit
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(DISTINCT user_id) FROM license_activations WHERE license_id=$1 AND revoked_at IS NULL`, licenseID).Scan(&userCount); err != nil {
		return err
	}
	var userAlreadyActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM license_activations WHERE license_id=$1 AND user_id=$2 AND revoked_at IS NULL)`, licenseID, userID).Scan(&userAlreadyActive); err != nil {
		return err
	}
	if !userAlreadyActive && userCount >= userLimit {
		return ErrLicenseUserLimit
	}
	_, err = tx.Exec(ctx, `INSERT INTO license_activations (id,license_id,deployment_id,user_id) VALUES ($1,$2,$3,$4)`, uuid.NewString(), licenseID, deploymentID, userID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) ModelScopes(ctx context.Context, deploymentID, userID string) ([]string, error) {
	rows, err := a.Pool.Query(ctx, `SELECT DISTINCT m.id
FROM models m
JOIN model_assignments ma ON ma.deployment_id=m.deployment_id AND ma.model_id=m.id
JOIN users u ON u.id=$2 AND u.deployment_id=$1
WHERE m.deployment_id=$1 AND m.enabled=true AND (
  (ma.subject_type='user' AND ma.subject_id=$2)
  OR (ma.subject_type='role' AND EXISTS (
    SELECT 1 FROM user_role_bindings urb
    JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true
    WHERE urb.deployment_id=$1 AND urb.user_id=u.id AND urb.role_id=ma.subject_id
  ))
  OR (ma.subject_type='team' AND EXISTS (
    SELECT 1 FROM user_team_bindings utb
    JOIN teams t ON t.deployment_id=utb.deployment_id AND t.id=utb.team_id AND t.enabled=true
    WHERE utb.deployment_id=$1 AND utb.user_id=u.id AND utb.team_id=ma.subject_id
  ))
)
ORDER BY m.id`, deploymentID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := make([]string, 0)
	for rows.Next() {
		var modelID string
		if err := rows.Scan(&modelID); err != nil {
			return nil, err
		}
		scopes = append(scopes, modelID)
	}
	return scopes, rows.Err()
}

func (a *App) UserRoleIDs(ctx context.Context, deploymentID, userID string) ([]string, error) {
	return a.Store.Deployment(deploymentID).UserRoleIDs(ctx, userID)
}

// IssueUserSession creates a refreshable terminal session that is scoped to a
// user topic. It does not create or touch a legacy Agent record.
func (a *App) IssueUserSession(ctx context.Context, user repository.User) (TokenResponse, error) {
	if a.Pool == nil {
		return TokenResponse{}, errors.New("database is unavailable")
	}
	sessionID := uuid.NewString()
	topic := fmt.Sprintf("user:%s:%s", a.DeploymentID(), user.ID)
	modelScopes, err := a.ModelScopes(ctx, user.DeploymentID, user.ID)
	if err != nil {
		return TokenResponse{}, err
	}
	if user.RequirePasswordChange {
		modelScopes = nil
	}
	roles, err := a.UserRoleIDs(ctx, user.DeploymentID, user.ID)
	if err != nil {
		return TokenResponse{}, err
	}
	access, model, err := a.Tokens.IssueWithDeploymentSession(user.ID, a.DeploymentID(), sessionID, user.IsAdmin, user.RequirePasswordChange, roles, modelScopes)
	if err != nil {
		return TokenResponse{}, err
	}
	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return TokenResponse{}, err
	}
	expires := time.Now().UTC().Add(a.Config.RefreshTTL)
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return TokenResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO user_sessions (session_id,deployment_id,user_id,topic) VALUES ($1,$2,$3,$4)`, sessionID, a.DeploymentID(), user.ID, topic); err != nil {
		return TokenResponse{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_session_tokens (token_hash,session_id,expires_at) VALUES ($1,$2,$3)`, refreshHash, sessionID, expires); err != nil {
		return TokenResponse{}, err
	}
	// Every active event addressed to this user (or one of their Teams) gets a
	// private delivery cursor. Acknowledging one terminal must never consume a
	// sibling terminal's copy.
	if _, err := tx.Exec(ctx, `
INSERT INTO session_control_deliveries (delivery_id,event_id,session_id)
SELECT gen_random_uuid()::text,e.event_id,$1
FROM control_events e
WHERE e.deployment_id=$2 AND e.state='active' AND e.expires_at>now()
  AND (e.scope_type='global' OR (e.scope_type='user' AND e.scope_id=$3)
    OR (e.scope_type='team' AND EXISTS (SELECT 1 FROM user_team_bindings utb WHERE utb.deployment_id=$2 AND utb.user_id=$3 AND utb.team_id=e.scope_id)))
ON CONFLICT (event_id,session_id) DO NOTHING`, sessionID, a.DeploymentID(), user.ID); err != nil {
		return TokenResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{AccessToken: access, RefreshToken: refresh, ModelAccessToken: model, TokenType: "Bearer", ExpiresIn: int64(a.Config.AccessTTL.Seconds()), ModelAccessExpiresIn: int64(a.Config.ModelAccessTTL.Seconds()), DeploymentID: a.DeploymentID(), SessionID: sessionID, PasswordChangeRequired: user.RequirePasswordChange}, nil
}

// RefreshUserSession rotates a user-session refresh token and keeps its topic
// and session ID stable, allowing each terminal to maintain an independent
// event cursor.
func (a *App) RefreshUserSession(ctx context.Context, rawToken, requestedSessionID string) (TokenResponse, error) {
	hash := auth.HashRefreshToken(rawToken)
	tx, err := a.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TokenResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID, userID, userDeploymentID, deploymentID string
	var expires time.Time
	var revokedAt, sessionRevokedAt *time.Time
	var user repository.User
	err = tx.QueryRow(ctx, `SELECT t.session_id,s.user_id,u.deployment_id,s.deployment_id,t.expires_at,t.revoked_at,s.revoked_at,u.status,u.require_password_change,u.is_admin
FROM user_session_tokens t
JOIN user_sessions s ON s.session_id=t.session_id
JOIN users u ON u.id=s.user_id
	WHERE t.token_hash=$1 FOR UPDATE`, hash).Scan(
		&sessionID, &userID, &userDeploymentID, &deploymentID, &expires, &revokedAt, &sessionRevokedAt,
		&user.Status, &user.RequirePasswordChange, &user.IsAdmin,
	)
	if err != nil || revokedAt != nil || sessionRevokedAt != nil || time.Now().After(expires) || (requestedSessionID != "" && requestedSessionID != sessionID) {
		return TokenResponse{}, ErrRefreshTokenInvalid
	}
	user.ID = userID
	user.DeploymentID = userDeploymentID
	if user.Status != "active" {
		return TokenResponse{}, ErrRefreshTokenInvalid
	}
	modelScopes, err := a.ModelScopes(ctx, userDeploymentID, user.ID)
	if err != nil {
		return TokenResponse{}, err
	}
	if user.RequirePasswordChange {
		modelScopes = nil
	}
	roles, err := a.UserRoleIDs(ctx, userDeploymentID, user.ID)
	if err != nil {
		return TokenResponse{}, err
	}
	access, model, err := a.Tokens.IssueWithDeploymentSession(user.ID, deploymentID, sessionID, user.IsAdmin, user.RequirePasswordChange, roles, modelScopes)
	if err != nil {
		return TokenResponse{}, err
	}
	refresh, newHash, err := auth.NewRefreshToken()
	if err != nil {
		return TokenResponse{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_session_tokens SET revoked_at=now() WHERE token_hash=$1`, hash); err != nil {
		return TokenResponse{}, err
	}
	newExpires := time.Now().UTC().Add(a.Config.RefreshTTL)
	if _, err := tx.Exec(ctx, `INSERT INTO user_session_tokens (token_hash,session_id,expires_at) VALUES ($1,$2,$3)`, newHash, sessionID, newExpires); err != nil {
		return TokenResponse{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_sessions SET last_seen_at=now() WHERE session_id=$1`, sessionID); err != nil {
		return TokenResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{AccessToken: access, RefreshToken: refresh, ModelAccessToken: model, TokenType: "Bearer", ExpiresIn: int64(a.Config.AccessTTL.Seconds()), ModelAccessExpiresIn: int64(a.Config.ModelAccessTTL.Seconds()), DeploymentID: deploymentID, SessionID: sessionID, PasswordChangeRequired: user.RequirePasswordChange}, nil
}

func (a *App) RevokeUserSession(ctx context.Context, rawToken, sessionID string) error {
	if a.Pool == nil {
		return nil
	}
	hash := auth.HashRefreshToken(rawToken)
	if sessionID == "" {
		return nil
	}
	_, err := a.Pool.Exec(ctx, `UPDATE user_session_tokens SET revoked_at=now() WHERE token_hash=$1 AND session_id=$2`, hash, sessionID)
	if err != nil {
		return err
	}
	_, err = a.Pool.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE session_id=$1`, sessionID)
	return err
}

func (a *App) RevokeUserSessionSet(ctx context.Context, userID string) error {
	if a.Pool == nil {
		return nil
	}
	if _, err := a.Pool.Exec(ctx, `UPDATE user_session_tokens SET revoked_at=now() WHERE session_id IN (SELECT session_id FROM user_sessions WHERE user_id=$1) AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	_, err := a.Pool.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID)
	return err
}

func (a *App) bootstrap(ctx context.Context) error {
	connection, err := a.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	const lockID int64 = 0x4145505F424F4F54
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("acquire bootstrap lock: %w", err)
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := connection.Exec(unlockContext, `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			_ = connection.Conn().Close(unlockContext)
		}
	}()
	if _, err := a.Store.UpsertDeployment(ctx, repository.Deployment{ID: a.DeploymentID(), Name: a.DeploymentName()}); err != nil {
		return fmt.Errorf("bootstrap deployment: %w", err)
	}
	if _, err := a.Store.UpsertDeployment(ctx, repository.Deployment{ID: a.Config.BootstrapDeploymentID, Name: a.Config.BootstrapDeploymentName}); err != nil {
		return err
	}
	if _, err := connection.Exec(ctx, `INSERT INTO roles (deployment_id, id, name, built_in) VALUES ($1, 'admin', 'Administrator', true) ON CONFLICT (deployment_id, id) DO NOTHING`, a.Config.BootstrapDeploymentID); err != nil {
		return fmt.Errorf("bootstrap administrator role definition: %w", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO role_permissions (deployment_id, role_id, permission_id) SELECT $1, 'admin', id FROM permissions ON CONFLICT DO NOTHING`, a.Config.BootstrapDeploymentID); err != nil {
		return fmt.Errorf("bootstrap administrator permissions: %w", err)
	}
	if _, err := connection.Exec(ctx, `INSERT INTO teams (deployment_id, id, name, description, built_in) VALUES ($1, 'all-users', 'All users', 'Default team for every deployment user', true) ON CONFLICT (deployment_id, id) DO NOTHING`, a.Config.BootstrapDeploymentID); err != nil {
		return fmt.Errorf("bootstrap default team: %w", err)
	}
	deploymentStore := a.Store.Deployment(a.Config.BootstrapDeploymentID)
	user, err := deploymentStore.GetUserByUsername(ctx, a.Config.BootstrapAdminUsername)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if errors.Is(err, repository.ErrNotFound) {
		passwordHash, hashErr := auth.HashPassword(a.Config.BootstrapAdminPassword)
		if hashErr != nil {
			return hashErr
		}
		created, createErr := deploymentStore.CreateUser(ctx, repository.CreateUserParams{
			ID: uuid.NewString(), Username: a.Config.BootstrapAdminUsername,
			DisplayName: a.Config.BootstrapAdminDisplayName, PasswordHash: passwordHash,
			RequirePasswordChange: false, IsAdmin: true,
		})
		user, err = created.User, createErr
		if err != nil {
			return fmt.Errorf("bootstrap administrator: %w", err)
		}
	}
	if _, err = connection.Exec(ctx, `INSERT INTO user_role_bindings (deployment_id,user_id,role_id,is_primary) VALUES ($1,$2,'admin',true) ON CONFLICT DO NOTHING`, a.Config.BootstrapDeploymentID, user.ID); err != nil {
		return fmt.Errorf("bootstrap administrator role: %w", err)
	}
	if _, err = connection.Exec(ctx, `INSERT INTO user_team_bindings (deployment_id,user_id,team_id,is_primary) VALUES ($1,$2,'all-users',true) ON CONFLICT DO NOTHING`, a.Config.BootstrapDeploymentID, user.ID); err != nil {
		return fmt.Errorf("bootstrap administrator team: %w", err)
	}
	return nil
}
