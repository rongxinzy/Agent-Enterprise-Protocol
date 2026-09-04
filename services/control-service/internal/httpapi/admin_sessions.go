package httpapi

import (
	"net/http"
	"time"
)

// listUserSessions exposes terminal sessions as the canonical operational
// identity. It intentionally returns no refresh-token material.
func (s *Server) listUserSessions(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Pool.Query(request.Context(), `
SELECT session_id,user_id,topic,created_at,last_seen_at,revoked_at
FROM user_sessions
WHERE deployment_id=$1 AND ($2='' OR user_id=$2)
ORDER BY last_seen_at DESC LIMIT $3`, claimsFrom(request).DeploymentID, request.URL.Query().Get("userId"), limit(request))
	if err != nil {
		databaseFailure(response, request, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var sessionID, userID, topic string
		var createdAt, lastSeenAt time.Time
		var revokedAt *time.Time
		if err := rows.Scan(&sessionID, &userID, &topic, &createdAt, &lastSeenAt, &revokedAt); err != nil {
			databaseFailure(response, request, err)
			return
		}
		items = append(items, map[string]any{
			"sessionId": sessionID, "userId": userID, "topic": topic,
			"createdAt": createdAt, "lastSeenAt": lastSeenAt, "revokedAt": revokedAt,
		})
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nil})
}
