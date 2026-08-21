package app

import (
	"context"
	"errors"
	"fmt"
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
)

var (
	ErrAgentConflict       = errors.New("agent identifier is already bound to another identity")
	ErrRefreshTokenInvalid = errors.New("refresh token is invalid or expired")
)

type App struct {
	Config      config.Config
	Pool        *pgxpool.Pool
	DB          *db.Queries
	Blobs       blob.Store
	Tokens      *auth.Service
	Credentials *credential.Sealer
}

type AgentContext struct {
	AgentID      string `json:"agentId"`
	AgentVersion string `json:"agentVersion"`
	Platform     string `json:"platform"`
}

type TokenResponse struct {
	AccessToken          string `json:"accessToken"`
	RefreshToken         string `json:"refreshToken"`
	ModelAccessToken     string `json:"modelAccessToken"`
	TokenType            string `json:"tokenType"`
	ExpiresIn            int64  `json:"expiresIn"`
	ModelAccessExpiresIn int64  `json:"modelAccessExpiresIn"`
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
	application := &App{Config: cfg, Pool: pool, DB: db.New(pool), Blobs: blobs, Tokens: tokens, Credentials: credentials}
	if err := application.bootstrap(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return application, nil
}

func (a *App) Close() {
	a.Pool.Close()
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
	return a.DB.CreateAgent(ctx, db.CreateAgentParams{AgentID: agent.AgentID, EnterpriseID: user.EnterpriseID, UserID: user.ID, AgentVersion: agent.AgentVersion, Platform: agent.Platform})
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
	access, model, err := a.Tokens.Issue(user.ID, user.EnterpriseID, agent.AgentID, user.IsAdmin, user.RoleIds, modelScopes)
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
	return TokenResponse{AccessToken: access, RefreshToken: refresh, ModelAccessToken: model, TokenType: "Bearer", ExpiresIn: int64(a.Config.AccessTTL.Seconds()), ModelAccessExpiresIn: int64(a.Config.ModelAccessTTL.Seconds())}, nil
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
	access, model, err := a.Tokens.Issue(user.ID, enterpriseID, agentID, user.IsAdmin, user.RoleIds, modelScopes)
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
	return TokenResponse{AccessToken: access, RefreshToken: refresh, ModelAccessToken: model, TokenType: "Bearer", ExpiresIn: int64(a.Config.AccessTTL.Seconds()), ModelAccessExpiresIn: int64(a.Config.ModelAccessTTL.Seconds())}, nil
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
