package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

const retentionAdvisoryLockID int64 = 0x4145505F5245544E

type RetentionResult struct {
	LockAcquired              bool
	AuthenticationAudit       int64
	CredentialResolutionAudit int64
	LicenseAudit              int64
	Telemetry                 int64
	SkillSync                 int64
	ControlDeliveries         int64
	ControlEvents             int64
	SessionTokens             int64
	Sessions                  int64
	LoginRateLimits           int64
}

func (result RetentionResult) Total() int64 {
	return result.AuthenticationAudit + result.CredentialResolutionAudit + result.LicenseAudit +
		result.Telemetry + result.SkillSync + result.ControlDeliveries + result.ControlEvents +
		result.SessionTokens + result.Sessions + result.LoginRateLimits
}

func (a *App) CleanupRetention(ctx context.Context, now time.Time) (result RetentionResult, err error) {
	database := a.database()
	if database == nil {
		return result, errors.New("retention cleanup database is unavailable")
	}
	batchSize := a.Config.RetentionCleanupBatchSize
	if batchSize <= 0 {
		return result, errors.New("retention cleanup batch size must be positive")
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin retention cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, retentionAdvisoryLockID).Scan(&result.LockAcquired); err != nil {
		return result, fmt.Errorf("acquire retention cleanup lock: %w", err)
	}
	if !result.LockAcquired {
		return result, nil
	}

	if retention := a.Config.AuditRetention; retention > 0 {
		cutoff := now.Add(-retention)
		if result.AuthenticationAudit, err = retentionDelete(ctx, tx, "authentication audit", `DELETE FROM authentication_audit_events WHERE cursor IN (SELECT cursor FROM authentication_audit_events WHERE created_at<$1 ORDER BY created_at LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
		if result.CredentialResolutionAudit, err = retentionDelete(ctx, tx, "credential resolution audit", `DELETE FROM credential_resolution_audit WHERE id IN (SELECT id FROM credential_resolution_audit WHERE created_at<$1 ORDER BY created_at LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
		if result.LicenseAudit, err = retentionDelete(ctx, tx, "license audit", `DELETE FROM license_audit_events WHERE id IN (SELECT id FROM license_audit_events WHERE created_at<$1 ORDER BY created_at LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
	}

	if retention := a.Config.TelemetryRetention; retention > 0 {
		cutoff := now.Add(-retention)
		if result.Telemetry, err = retentionDelete(ctx, tx, "telemetry", `DELETE FROM telemetry_events WHERE event_id IN (SELECT event_id FROM telemetry_events WHERE received_at<$1 ORDER BY received_at LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
		if result.SkillSync, err = retentionDelete(ctx, tx, "Skill sync", `DELETE FROM skill_sync_results WHERE id IN (SELECT id FROM skill_sync_results WHERE created_at<$1 ORDER BY created_at LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
	}

	if retention := a.Config.OperationalRetention; retention > 0 {
		cutoff := now.Add(-retention)
		if result.ControlDeliveries, err = retentionDelete(ctx, tx, "control deliveries", `DELETE FROM session_control_deliveries WHERE delivery_id IN (SELECT delivery_id FROM session_control_deliveries WHERE state IN ('succeeded','expired','superseded') AND updated_at<$1 ORDER BY updated_at LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
		if result.ControlEvents, err = retentionDelete(ctx, tx, "control events", `DELETE FROM control_events WHERE event_id IN (SELECT event_id FROM control_events WHERE (state<>'active' OR expires_at<$3) AND GREATEST(created_at,expires_at)<$1 ORDER BY GREATEST(created_at,expires_at) LIMIT $2)`, cutoff, batchSize, now); err != nil {
			return result, err
		}
		if result.SessionTokens, err = retentionDelete(ctx, tx, "session tokens", `DELETE FROM user_session_tokens WHERE token_hash IN (SELECT token_hash FROM user_session_tokens WHERE expires_at<$1 OR revoked_at<$1 ORDER BY LEAST(expires_at,COALESCE(revoked_at,expires_at)) LIMIT $2)`, cutoff, batchSize); err != nil {
			return result, err
		}
		if result.Sessions, err = retentionDelete(ctx, tx, "sessions", `DELETE FROM user_sessions WHERE session_id IN (SELECT session_id FROM user_sessions s WHERE GREATEST(last_seen_at,COALESCE(revoked_at,last_seen_at))<$1 AND (revoked_at IS NOT NULL OR NOT EXISTS (SELECT 1 FROM user_session_tokens t WHERE t.session_id=s.session_id AND t.revoked_at IS NULL AND t.expires_at>$3)) ORDER BY GREATEST(last_seen_at,COALESCE(revoked_at,last_seen_at)) LIMIT $2)`, cutoff, batchSize, now); err != nil {
			return result, err
		}
	}

	loginRetention := max(a.Config.LoginFailureWindow, a.Config.LoginBackoffMax)
	if loginRetention > 0 {
		if result.LoginRateLimits, err = retentionDelete(ctx, tx, "login rate limits", `DELETE FROM login_rate_limits WHERE key_hash IN (SELECT key_hash FROM login_rate_limits WHERE updated_at<$1 ORDER BY updated_at LIMIT $2)`, now.Add(-loginRetention), batchSize); err != nil {
			return result, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit retention cleanup: %w", err)
	}
	return result, nil
}

func retentionDelete(ctx context.Context, tx pgx.Tx, name, statement string, arguments ...any) (int64, error) {
	tag, err := tx.Exec(ctx, statement, arguments...)
	if err != nil {
		return 0, fmt.Errorf("delete expired %s: %w", name, err)
	}
	return tag.RowsAffected(), nil
}

func (a *App) RunRetention(ctx context.Context) {
	interval := a.Config.RetentionCleanupInterval
	if interval <= 0 {
		return
	}
	run := func() {
		cleanupContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, err := a.CleanupRetention(cleanupContext, time.Now().UTC())
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("retention cleanup failed", "error", err)
			}
			return
		}
		if result.LockAcquired && result.Total() > 0 {
			slog.Info("retention cleanup completed",
				"direct_rows", result.Total(),
				"authentication_audit", result.AuthenticationAudit,
				"credential_resolution_audit", result.CredentialResolutionAudit,
				"license_audit", result.LicenseAudit,
				"telemetry", result.Telemetry,
				"skill_sync", result.SkillSync,
				"control_deliveries", result.ControlDeliveries,
				"control_events", result.ControlEvents,
				"session_tokens", result.SessionTokens,
				"sessions", result.Sessions,
				"login_rate_limits", result.LoginRateLimits,
			)
		}
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
