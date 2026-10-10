package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// Administrators change shared content (a Skill and its version packages) on
// behalf of the whole deployment, so every mutation attempt is appended to
// telemetry_events - the same store the console's operation log reads - which
// makes "who changed which Skill, when, and with what result" answerable after
// the fact.
//
// Auditing is best effort: a failed audit write must never fail the action it
// describes. A deployment key authenticates the deployment rather than a
// person, so there is no actor to attribute and nothing is recorded.
func (s *Server) recordSkillAudit(request *http.Request, eventType, resourceType, resourceID, result string, payload map[string]any) {
	claims := claimsFrom(request)
	if claims.Subject == "" {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var session any
	if claims.SessionID != "" {
		session = claims.SessionID
	}
	database := s.app.Database()
	if database == nil {
		// No runtime database is wired (degraded or test harness): there is
		// nowhere to record the action, and the action itself must still run.
		return
	}
	_, err = database.Exec(request.Context(), `INSERT INTO telemetry_events(event_id,deployment_id,user_id,session_id,type,resource_type,resource_id,result,payload,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`,
		uuid.NewString(), claims.DeploymentID, claims.Subject, session, eventType, resourceType, resourceID, result, data)
	if err != nil {
		// Auditing never fails the action it describes, but a persistent audit
		// failure must be visible in the service log rather than silently
		// dropped.
		slog.Error("skill audit failed", "event", eventType, "skill_id", resourceID, "actor", claims.Subject, "error", err)
	}
}

// skillChangedFields reports the fields an update actually asked to change, so
// the audit entry says what was touched rather than dumping the whole patch.
func skillChangedFields(name, description *string, enabled *bool) map[string]any {
	changed := map[string]any{}
	if name != nil {
		changed["name"] = *name
	}
	if description != nil {
		changed["description"] = *description
	}
	if enabled != nil {
		changed["enabled"] = *enabled
	}
	return changed
}
