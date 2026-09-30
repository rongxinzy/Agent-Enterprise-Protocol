package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

// listUserSessions exposes terminal sessions as the canonical operational
// identity. It intentionally returns no refresh-token material. The client
// field carries the self-reported or User-Agent-derived client identity
// recorded at login, or null when none is known.
func (s *Server) listUserSessions(response http.ResponseWriter, request *http.Request) {
	rows, err := s.app.Database().Query(request.Context(), `
SELECT session_id,user_id,topic,created_at,last_seen_at,revoked_at,client_name,client_version,client_device_id
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
		var clientName, clientVersion, clientDeviceID pgtype.Text
		if err := rows.Scan(&sessionID, &userID, &topic, &createdAt, &lastSeenAt, &revokedAt, &clientName, &clientVersion, &clientDeviceID); err != nil {
			databaseFailure(response, request, err)
			return
		}
		client := app.SessionClient{Name: pgTextPointer(clientName), Version: pgTextPointer(clientVersion), DeviceID: pgTextPointer(clientDeviceID)}
		items = append(items, map[string]any{
			"sessionId": sessionID, "userId": userID, "topic": topic,
			"createdAt": createdAt, "lastSeenAt": lastSeenAt, "revokedAt": revokedAt,
			"client": sessionClientJSON(client),
		})
	}
	if err := rows.Err(); err != nil {
		databaseFailure(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "nextCursor": nil})
}

func (s *Server) revokeUserSession(response http.ResponseWriter, request *http.Request) {
	if err := s.app.RevokeUserSessionByID(request.Context(), claimsFrom(request).DeploymentID, chi.URLParam(request, "sessionId")); errors.Is(err, app.ErrSessionNotFound) {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The user session was not found.")
		return
	} else if err != nil {
		databaseFailure(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
