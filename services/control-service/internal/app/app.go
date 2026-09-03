package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/blob"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/credential"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	db "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db/generated"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
)

var (
	ErrAgentConflict        = errors.New("agent identifier is already bound to another identity")
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
	DB              *db.Queries
	Blobs           blob.Store
	Tokens          *auth.Service
	Credentials     *credential.Sealer
	LicenseVerifier *license.Verifier
	License         *license.Verified
	licenseMu       sync.RWMutex
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

type AgentContext struct {
	AgentID      string `json:"agentId"`
	AgentVersion string `json:"agentVersion"`
	Platform     string `json:"platform"`
}

type TokenResponse struct {
	AccessToken            string `json:"accessToken"`
	RefreshToken           string `json:"refreshToken"`
	ModelAccessToken       string `json:"modelAccessToken"`
	TokenType              string `json:"tokenType"`
	ExpiresIn              int64  `json:"expiresIn"`
	ModelAccessExpiresIn   int64  `json:"modelAccessExpiresIn"`
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
	application := &App{Config: cfg, Pool: pool, DB: db.New(pool), Blobs: blobs, Tokens: tokens, Credentials: credentials, LicenseVerifier: licenseVerifier}
	if cfg.LicenseFile != "" {
		licenseBytes, readErr := os.ReadFile(cfg.LicenseFile)
		if readErr != nil {
			pool.Close()
			return nil, fmt.Errorf("read AEP_LICENSE_FILE: %w", readErr)
		}
		verified, verifyErr := licenseVerifier.Verify(licenseBytes)
		if verifyErr != nil || verified.Status == "enterprise-expired" || (cfg.LicenseCustomerID != "" && verified.Claims.CustomerID != cfg.LicenseCustomerID) {
			pool.Close()
			if verifyErr != nil {
				return nil, fmt.Errorf("verify AEP_LICENSE_FILE: %w", verifyErr)
			}
			return nil, errors.New("AEP_LICENSE_FILE is expired or bound to another customer")
		}
		application.SetLicense(verified)
	}
	if err := application.bootstrap(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if application.License != nil {
		if err := application.RegisterLicense(ctx, *application.License); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return application, nil
}

func (a *App) Close() {
	a.Pool.Close()
}

func (a *App) RegisterLicense(ctx context.Context, verified license.Verified) error {
	if a.Pool == nil {
		return nil
	}
	claims := verified.Claims
	if a.Config.LicenseEnterpriseID == "" {
		return errors.New("AEP_LICENSE_ENTERPRISE_ID is required to register a license")
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
	_, err = a.Pool.Exec(ctx, `INSERT INTO licenses (license_id,enterprise_id,customer_id,deployment_id,digest,key_id,issued_at,expires_at,grace_ends_at,user_limit,agent_limit,features,payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, claims.LicenseID, a.Config.LicenseEnterpriseID, claims.CustomerID, claims.DeploymentID, verified.Digest, verified.Envelope.KeyID, issuedAt, expiresAt, graceEndsAt, claims.Limits.Users, claims.Limits.Agents, claims.Features, verified.Envelope.Payload)
	return err
}

func (a *App) ActivateLicense(ctx context.Context, licenseID, enterpriseID, userID, agentID string) error {
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
	var agentLimit, userLimit int
	err = tx.QueryRow(ctx, `SELECT status, revoked_at, agent_limit, user_limit FROM licenses WHERE license_id=$1 AND enterprise_id=$2 FOR UPDATE`, licenseID, enterpriseID).Scan(&status, &revokedAt, &agentLimit, &userLimit)
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
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM license_activations WHERE license_id=$1 AND agent_id=$2 AND revoked_at IS NULL)`, licenseID, agentID).Scan(&alreadyActive); err != nil {
		return err
	}
	if alreadyActive {
		_, err = tx.Exec(ctx, `UPDATE license_activations SET last_seen_at=now() WHERE license_id=$1 AND agent_id=$2 AND revoked_at IS NULL`, licenseID, agentID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var agentCount, userCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM license_activations WHERE license_id=$1 AND revoked_at IS NULL`, licenseID).Scan(&agentCount); err != nil {
		return err
	}
	if agentCount >= agentLimit {
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
	_, err = tx.Exec(ctx, `INSERT INTO license_activations (id,license_id,enterprise_id,user_id,agent_id) VALUES ($1,$2,$3,$4,$5)`, uuid.NewString(), licenseID, enterpriseID, userID, agentID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) BindAgent(ctx context.Context, user db.User, agent AgentContext) (db.Agent, error) {
	existing, err := a.DB.GetAgent(ctx, agent.AgentID)
	if err == nil {
		if existing.UserID != user.ID || existing.EnterpriseID != user.EnterpriseID {
			return db.Agent{}, ErrAgentConflict
		}
		return a.DB.TouchAgent(ctx, db.TouchAgentParams{AgentID: agent.AgentID, AgentVersion: agent.AgentVersion, Platform: agent.Platform})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Agent{}, err
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return db.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	created, err := a.DB.WithTx(tx).CreateAgent(ctx, db.CreateAgentParams{AgentID: agent.AgentID, EnterpriseID: user.EnterpriseID, UserID: user.ID, AgentVersion: agent.AgentVersion, Platform: agent.Platform})
	if err != nil {
		return db.Agent{}, err
	}
	rows, err := tx.Query(ctx, `SELECT e.event_id
FROM control_events e
WHERE e.enterprise_id=$1 AND e.state='active' AND e.expires_at>now()
AND (e.scope_type='global' OR (e.scope_type='agent' AND e.scope_id=$2) OR (e.scope_type='user' AND e.scope_id=$3) OR (e.scope_type='organization' AND e.scope_id=ANY($4::text[])))`, user.EnterpriseID, agent.AgentID, user.ID, user.OrganizationIds)
	if err != nil {
		return db.Agent{}, err
	}
	eventIDs := make([]string, 0)
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			rows.Close()
			return db.Agent{}, err
		}
		eventIDs = append(eventIDs, eventID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return db.Agent{}, err
	}
	rows.Close()
	for _, eventID := range eventIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO control_deliveries (delivery_id,event_id,agent_id) VALUES ($1,$2,$3) ON CONFLICT (event_id,agent_id) DO NOTHING`, uuid.NewString(), eventID, agent.AgentID); err != nil {
			return db.Agent{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Agent{}, err
	}
	return created, nil
}

func (a *App) ModelScopes(ctx context.Context, enterpriseID, userID, agentID string) ([]string, error) {
	rows, err := a.Pool.Query(ctx, `SELECT DISTINCT m.id
FROM models m
JOIN model_assignments ma ON ma.enterprise_id=m.enterprise_id AND ma.model_id=m.id
JOIN users u ON u.id=$2 AND u.enterprise_id=$1
WHERE m.enterprise_id=$1 AND m.enabled=true AND (
  (ma.subject_type='enterprise' AND ma.subject_id=$1)
  OR (ma.subject_type='organization' AND ma.subject_id=ANY(u.organization_ids))
  OR (ma.subject_type='user' AND ma.subject_id=$2)
  OR (ma.subject_type='agent' AND ma.subject_id=$3)
)
ORDER BY m.id`, enterpriseID, userID, agentID)
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

func (a *App) IssueSession(ctx context.Context, user db.User, agent AgentContext) (TokenResponse, error) {
	if _, err := a.BindAgent(ctx, user, agent); err != nil {
		return TokenResponse{}, err
	}
	modelScopes, err := a.ModelScopes(ctx, user.EnterpriseID, user.ID, agent.AgentID)
	if err != nil {
		return TokenResponse{}, err
	}
	if user.RequirePasswordChange {
		modelScopes = nil
	}
	access, model, err := a.Tokens.Issue(user.ID, user.EnterpriseID, agent.AgentID, user.IsAdmin, user.RequirePasswordChange, user.RoleIds, modelScopes)
	if err != nil {
		return TokenResponse{}, err
	}
	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return TokenResponse{}, err
	}
	expires := time.Now().UTC().Add(a.Config.RefreshTTL)
	if err := a.DB.CreateRefreshSession(ctx, db.CreateRefreshSessionParams{TokenHash: refreshHash, EnterpriseID: user.EnterpriseID, UserID: user.ID, AgentID: agent.AgentID, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}}); err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{AccessToken: access, RefreshToken: refresh, ModelAccessToken: model, TokenType: "Bearer", ExpiresIn: int64(a.Config.AccessTTL.Seconds()), ModelAccessExpiresIn: int64(a.Config.ModelAccessTTL.Seconds()), PasswordChangeRequired: user.RequirePasswordChange}, nil
}

func (a *App) RefreshSession(ctx context.Context, rawToken, agentID string) (TokenResponse, error) {
	hash := auth.HashRefreshToken(rawToken)
	tx, err := a.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TokenResponse{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID, enterpriseID, storedAgentID string
	var expires time.Time
	var revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT user_id, enterprise_id, agent_id, expires_at, revoked_at FROM refresh_sessions WHERE token_hash=$1 FOR UPDATE`, hash).Scan(&userID, &enterpriseID, &storedAgentID, &expires, &revoked)
	if err != nil || revoked != nil || time.Now().After(expires) || storedAgentID != agentID {
		return TokenResponse{}, ErrRefreshTokenInvalid
	}
	var user db.User
	queries := db.New(tx)
	user, err = queries.GetUser(ctx, userID)
	if err != nil || user.Status != "active" {
		return TokenResponse{}, ErrRefreshTokenInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_sessions SET revoked_at=now() WHERE token_hash=$1`, hash); err != nil {
		return TokenResponse{}, err
	}
	modelScopes, err := a.ModelScopes(ctx, enterpriseID, user.ID, agentID)
	if err != nil {
		return TokenResponse{}, err
	}
	if user.RequirePasswordChange {
		modelScopes = nil
	}
	access, model, err := a.Tokens.Issue(user.ID, enterpriseID, agentID, user.IsAdmin, user.RequirePasswordChange, user.RoleIds, modelScopes)
	if err != nil {
		return TokenResponse{}, err
	}
	refresh, newHash, err := auth.NewRefreshToken()
	if err != nil {
		return TokenResponse{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_sessions (token_hash, enterprise_id, user_id, agent_id, expires_at) VALUES ($1,$2,$3,$4,$5)`, newHash, enterpriseID, userID, agentID, time.Now().UTC().Add(a.Config.RefreshTTL)); err != nil {
		return TokenResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{AccessToken: access, RefreshToken: refresh, ModelAccessToken: model, TokenType: "Bearer", ExpiresIn: int64(a.Config.AccessTTL.Seconds()), ModelAccessExpiresIn: int64(a.Config.ModelAccessTTL.Seconds()), PasswordChangeRequired: user.RequirePasswordChange}, nil
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
	queries := db.New(connection)
	if _, err := queries.CreateEnterprise(ctx, db.CreateEnterpriseParams{ID: a.Config.BootstrapEnterpriseID, Name: a.Config.BootstrapEnterpriseName}); err != nil {
		return err
	}
	_, err = queries.GetUserByUsername(ctx, db.GetUserByUsernameParams{EnterpriseID: a.Config.BootstrapEnterpriseID, Username: a.Config.BootstrapAdminUsername})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	passwordHash, err := auth.HashPassword(a.Config.BootstrapAdminPassword)
	if err != nil {
		return err
	}
	_, err = queries.CreateUser(ctx, db.CreateUserParams{ID: uuid.NewString(), EnterpriseID: a.Config.BootstrapEnterpriseID, Username: a.Config.BootstrapAdminUsername, DisplayName: a.Config.BootstrapAdminDisplayName, PasswordHash: passwordHash, RequirePasswordChange: false, IsAdmin: true, OrganizationIds: []string{}, RoleIds: []string{"admin"}})
	if err != nil {
		return fmt.Errorf("bootstrap administrator: %w", err)
	}
	return nil
}
