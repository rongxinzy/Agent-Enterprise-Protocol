package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type loginFingerprint struct {
	PrincipalSourceKeyHash string
	SourceKeyHash          string
	PrincipalHash          string
	SourceHash             string
}

func (s *Server) loginFingerprint(request *http.Request, enterpriseID, username string) loginFingerprint {
	source := s.loginSource(request)
	principal := opaqueHash("principal", enterpriseID, strings.ToLower(strings.TrimSpace(username)))
	sourceHash := opaqueHash("source", source)
	return loginFingerprint{
		PrincipalSourceKeyHash: opaqueHash("login-principal-source", principal, sourceHash),
		SourceKeyHash:          opaqueHash("login-source", sourceHash),
		PrincipalHash:          principal,
		SourceHash:             sourceHash,
	}
}

func (s *Server) loginSource(request *http.Request) string {
	peer, ok := parseRemoteAddress(request.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if !addressInPrefixes(peer, s.app.Config.TrustedProxyCIDRs) {
		return peer.String()
	}
	forwarded := strings.TrimSpace(request.Header.Get("X-Forwarded-For"))
	if forwarded == "" {
		return peer.String()
	}
	chain := strings.Split(forwarded, ",")
	var leftmost netip.Addr
	for index := len(chain) - 1; index >= 0; index-- {
		address, err := netip.ParseAddr(strings.TrimSpace(chain[index]))
		if err != nil {
			return peer.String()
		}
		address = address.Unmap()
		leftmost = address
		if !addressInPrefixes(address, s.app.Config.TrustedProxyCIDRs) {
			return address.String()
		}
	}
	return leftmost.String()
}

func parseRemoteAddress(value string) (netip.Addr, bool) {
	if addressPort, err := netip.ParseAddrPort(value); err == nil {
		return addressPort.Addr().Unmap(), true
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func addressInPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func opaqueHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(part))
		_, _ = digest.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func (s *Server) loginThrottle(ctx context.Context, fingerprint loginFingerprint, now time.Time) (time.Duration, error) {
	principalDelay, err := s.loginThrottleForKey(ctx, fingerprint.PrincipalSourceKeyHash, now)
	if err != nil {
		return 0, err
	}
	sourceDelay, err := s.loginThrottleForKey(ctx, fingerprint.SourceKeyHash, now)
	if err != nil {
		return 0, err
	}
	return max(principalDelay, sourceDelay), nil
}

func (s *Server) loginThrottleForKey(ctx context.Context, keyHash string, now time.Time) (time.Duration, error) {
	var blockedUntil pgtype.Timestamptz
	err := s.app.Database().QueryRow(ctx, `SELECT blocked_until FROM login_rate_limits WHERE key_hash=$1`, keyHash).Scan(&blockedUntil)
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

func (s *Server) recordLoginFailure(ctx context.Context, fingerprint loginFingerprint, enterpriseID, userID string, now time.Time) (time.Duration, error) {
	tx, err := s.app.Database().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	principalBackoff, err := s.recordLoginLimitFailure(ctx, tx, fingerprint.PrincipalSourceKeyHash, s.app.Config.LoginFailureLimit, now)
	if err != nil {
		return 0, err
	}
	sourceBackoff, err := s.recordLoginLimitFailure(ctx, tx, fingerprint.SourceKeyHash, s.app.Config.LoginSourceFailureLimit, now)
	if err != nil {
		return 0, err
	}
	backoff := max(principalBackoff, sourceBackoff)
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
	if err := insertAuthenticationAudit(ctx, tx, enterpriseID, userID, eventType, outcome, reason, fingerprint, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return backoff, nil
}

func (s *Server) recordLoginLimitFailure(ctx context.Context, tx pgx.Tx, keyHash string, failureLimit int, now time.Time) (time.Duration, error) {
	failureCount := 0
	if err := tx.QueryRow(ctx, `INSERT INTO login_rate_limits (key_hash,failure_count,blocked_until,updated_at) VALUES ($1,1,NULL,$2)
		ON CONFLICT (key_hash) DO UPDATE SET
		failure_count=CASE WHEN login_rate_limits.updated_at < $3 THEN 1 ELSE login_rate_limits.failure_count+1 END,
		blocked_until=NULL,updated_at=EXCLUDED.updated_at
		RETURNING failure_count`, keyHash, now, now.Add(-s.app.Config.LoginFailureWindow)).Scan(&failureCount); err != nil {
		return 0, err
	}
	backoff := loginBackoff(failureCount, failureLimit, s.app.Config.LoginBackoffBase, s.app.Config.LoginBackoffMax)
	var blockedUntil any
	if backoff > 0 {
		blockedUntil = now.Add(backoff)
	}
	if _, err := tx.Exec(ctx, `UPDATE login_rate_limits SET blocked_until=$2 WHERE key_hash=$1`, keyHash, blockedUntil); err != nil {
		return 0, err
	}
	return backoff, nil
}

func (s *Server) recordLoginThrottled(ctx context.Context, fingerprint loginFingerprint, enterpriseID string, now time.Time) error {
	return insertAuthenticationAudit(ctx, s.app.Database(), enterpriseID, "", "login.throttled", "denied", "backoff_active", fingerprint, now)
}

func (s *Server) recordLoginSuccess(ctx context.Context, fingerprint loginFingerprint, enterpriseID, userID string, now time.Time) {
	tx, err := s.app.Database().Begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `DELETE FROM login_rate_limits WHERE key_hash=$1`, fingerprint.PrincipalSourceKeyHash)
	}
	if err == nil {
		err = insertAuthenticationAudit(ctx, tx, enterpriseID, userID, "login.succeeded", "success", "", fingerprint, now)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		slog.Error("authentication audit failed", "event", "login.succeeded", "principal_hash", fingerprint.PrincipalHash, "error", err)
	}
}

func (s *Server) recordPasswordChanged(ctx context.Context, fingerprint loginFingerprint, enterpriseID, userID string, now time.Time) {
	if err := insertAuthenticationAudit(ctx, s.app.Database(), enterpriseID, userID, "password.changed", "success", "", fingerprint, now); err != nil {
		slog.Error("authentication audit failed", "event", "password.changed", "principal_hash", fingerprint.PrincipalHash, "error", err)
	}
}

type authenticationAuditExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertAuthenticationAudit(ctx context.Context, executor authenticationAuditExecutor, enterpriseID, userID, eventType, outcome, reason string, fingerprint loginFingerprint, now time.Time) error {
	var nullableUserID any
	if userID != "" {
		nullableUserID = userID
	}
	var nullableReason any
	if reason != "" {
		nullableReason = reason
	}
	_, err := executor.Exec(ctx, `INSERT INTO authentication_audit_events (deployment_id,user_id,event_type,outcome,reason,principal_hash,source_hash,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, enterpriseID, nullableUserID, eventType, outcome, nullableReason, fingerprint.PrincipalHash, fingerprint.SourceHash, now)
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
