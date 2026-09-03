package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type loginFingerprint struct {
	KeyHash       string
	PrincipalHash string
	SourceHash    string
}

func (s *Server) loginFingerprint(request *http.Request, enterpriseID, username string) loginFingerprint {
	source := request.RemoteAddr
	if host, _, err := net.SplitHostPort(source); err == nil {
		source = host
	}
	if source == "" {
		source = "unknown"
	}
	principal := opaqueHash("principal", enterpriseID, username)
	sourceHash := opaqueHash("source", source)
	return loginFingerprint{KeyHash: opaqueHash("login", principal), PrincipalHash: principal, SourceHash: sourceHash}
}

func opaqueHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(part))
		_, _ = digest.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func (s *Server) loginThrottle(ctx context.Context, keyHash string, now time.Time) (time.Duration, error) {
	var blockedUntil pgtype.Timestamptz
	err := s.app.Pool.QueryRow(ctx, `SELECT blocked_until FROM login_rate_limits WHERE key_hash=$1`, keyHash).Scan(&blockedUntil)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !blockedUntil.Valid) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if remaining := blockedUntil.Time.Sub(now); remaining > 0 {
		return remaining, nil
	}
	return 0, nil
}

func (s *Server) recordLoginFailure(ctx context.Context, fingerprint loginFingerprint, enterpriseID, userID, agentID string, now time.Time) (time.Duration, error) {
	tx, err := s.app.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	failureCount := 0
	if err := tx.QueryRow(ctx, `INSERT INTO login_rate_limits (key_hash,failure_count,blocked_until,updated_at) VALUES ($1,1,NULL,$2)
		ON CONFLICT (key_hash) DO UPDATE SET
		failure_count=CASE WHEN login_rate_limits.updated_at < $3 THEN 1 ELSE login_rate_limits.failure_count+1 END,
		blocked_until=NULL,updated_at=EXCLUDED.updated_at
		RETURNING failure_count`, fingerprint.KeyHash, now, now.Add(-s.app.Config.LoginFailureWindow)).Scan(&failureCount); err != nil {
		return 0, err
	}
	backoff := loginBackoff(failureCount, s.app.Config.LoginFailureLimit, s.app.Config.LoginBackoffBase, s.app.Config.LoginBackoffMax)
	var blockedUntil any
	if backoff > 0 {
		blockedUntil = now.Add(backoff)
	}
	if _, err := tx.Exec(ctx, `UPDATE login_rate_limits SET blocked_until=$2 WHERE key_hash=$1`, fingerprint.KeyHash, blockedUntil); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_rate_limits WHERE updated_at < $1`, now.Add(-2*s.app.Config.LoginFailureWindow)); err != nil {
		return 0, err
	}
	eventType := "login.failed"
	outcome := "failure"
	reason := "invalid_credentials"
	if backoff > 0 {
		eventType = "login.throttled"
		outcome = "denied"
		reason = "failure_limit_reached"
	}
	if err := insertAuthenticationAudit(ctx, tx, enterpriseID, userID, agentID, "", eventType, outcome, reason, fingerprint, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return backoff, nil
}

func (s *Server) recordLoginThrottled(ctx context.Context, fingerprint loginFingerprint, enterpriseID, agentID string, now time.Time) error {
	return insertAuthenticationAudit(ctx, s.app.Pool, enterpriseID, "", agentID, "", "login.throttled", "denied", "backoff_active", fingerprint, now)
}

func (s *Server) recordLoginSuccess(ctx context.Context, fingerprint loginFingerprint, enterpriseID, userID, agentID string, now time.Time) {
	tx, err := s.app.Pool.Begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `DELETE FROM login_rate_limits WHERE key_hash=$1`, fingerprint.KeyHash)
	}
	if err == nil {
		err = insertAuthenticationAudit(ctx, tx, enterpriseID, userID, agentID, "", "login.succeeded", "success", "", fingerprint, now)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		slog.Error("authentication audit failed", "event", "login.succeeded", "principal_hash", fingerprint.PrincipalHash, "error", err)
	}
}

func (s *Server) recordPasswordChanged(ctx context.Context, fingerprint loginFingerprint, enterpriseID, userID, agentID string, now time.Time) {
	if err := insertAuthenticationAudit(ctx, s.app.Pool, enterpriseID, userID, agentID, "", "password.changed", "success", "", fingerprint, now); err != nil {
		slog.Error("authentication audit failed", "event", "password.changed", "principal_hash", fingerprint.PrincipalHash, "error", err)
	}
}

type authenticationAuditExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertAuthenticationAudit(ctx context.Context, executor authenticationAuditExecutor, enterpriseID, userID, agentID, sessionID, eventType, outcome, reason string, fingerprint loginFingerprint, now time.Time) error {
	var nullableUserID any
	if userID != "" {
		nullableUserID = userID
	}
	var nullableReason any
	if reason != "" {
		nullableReason = reason
	}
	var nullableAgentID any
	if agentID != "" {
		nullableAgentID = boundedAuditID(agentID)
	}
	var nullableSessionID any
	if sessionID != "" {
		nullableSessionID = sessionID
	}
	_, err := executor.Exec(ctx, `INSERT INTO authentication_audit_events (deployment_id,user_id,agent_id,session_id,event_type,outcome,reason,principal_hash,source_hash,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, enterpriseID, nullableUserID, nullableAgentID, nullableSessionID, eventType, outcome, nullableReason, fingerprint.PrincipalHash, fingerprint.SourceHash, now)
	return err
}

func boundedAuditID(value string) string {
	if len(value) <= 256 {
		return value
	}
	return "sha256:" + opaqueHash(value)
}

func loginBackoff(failureCount, failureLimit int, base, maximum time.Duration) time.Duration {
	if failureCount < failureLimit {
		return 0
	}
	delay := base
	for step := failureLimit; step < failureCount; step++ {
		if delay >= maximum || delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func retryAfterSeconds(duration time.Duration) int64 {
	seconds := int64((duration + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
