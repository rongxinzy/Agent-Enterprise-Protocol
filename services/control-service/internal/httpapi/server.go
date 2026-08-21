package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/auth"
)

type contextKey string

const claimsContextKey contextKey = "aep-claims"

type federatedTransaction struct {
	EnterpriseID string
	State        string
	ExpiresAt    time.Time
}

type Server struct {
	app           *app.App
	router        chi.Router
	transactions  map[string]federatedTransaction
	transactionMu sync.Mutex
}

func New(application *app.App) *Server {
	server := &Server{app: application, transactions: make(map[string]federatedTransaction)}
	router := chi.NewRouter()
	router.Use(middleware.Recoverer, server.requestID)
	router.Get("/.well-known/jwks.json", server.getJWKS)
	router.Get("/healthz", server.health)
	router.Get("/aep/v1/metadata", server.metadata)
	router.Get("/aep/v1/auth/methods", server.authenticationMethods)
	router.Post("/aep/v1/auth/password/login", server.passwordLogin)
	router.Post("/aep/v1/auth/federated/start", server.federatedStart)
	router.Post("/aep/v1/auth/exchange", server.federatedExchange)
	router.Post("/aep/v1/auth/refresh", server.refreshSession)

	router.Group(func(protected chi.Router) {
		protected.Use(server.authenticate)
		protected.Post("/aep/v1/auth/password/change", server.changePassword)
		protected.Post("/aep/v1/auth/logout", server.logout)
		protected.Get("/aep/v1/agent/me", server.currentIdentity)
		protected.Get("/aep/v1/agent/models", server.listAgentModels)
		protected.Post("/aep/v1/agent/heartbeat", server.heartbeat)
		protected.Get("/aep/v1/agent/control-events", server.listAgentControlEvents)
		protected.Post("/aep/v1/agent/control-events/{deliveryId}/acknowledge", server.acknowledgeControlEvent)
		protected.Post("/aep/v1/agent/control-events/{deliveryId}/result", server.reportControlEventResult)
		protected.Get("/aep/v1/agent/skills/manifest", server.skillManifest)
		protected.Get("/aep/v1/agent/skills/{skillId}/versions/{version}/package", server.downloadSkillPackage)
		protected.Post("/aep/v1/agent/skills/sync-results", server.reportSkillSyncResult)
		protected.Post("/aep/v1/agent/events/batch", server.uploadTelemetryBatch)

		protected.Group(func(admin chi.Router) {
			admin.Use(server.requireAdmin)
			admin.Get("/aep/v1/admin/users", server.listUsers)
			admin.Post("/aep/v1/admin/users", server.createUser)
			admin.Post("/aep/v1/admin/users/import", server.importUsers)
			admin.Patch("/aep/v1/admin/users/{userId}", server.updateUser)
			admin.Post("/aep/v1/admin/users/{userId}/reset-password", server.resetUserPassword)
			admin.Get("/aep/v1/admin/skills", server.listSkills)
			admin.Post("/aep/v1/admin/skills", server.createSkill)
			admin.Get("/aep/v1/admin/skills/{skillId}", server.getSkill)
			admin.Patch("/aep/v1/admin/skills/{skillId}", server.updateSkill)
			admin.Delete("/aep/v1/admin/skills/{skillId}", server.deleteSkill)
			admin.Post("/aep/v1/admin/skills/{skillId}/versions", server.uploadSkillVersion)
			admin.Post("/aep/v1/admin/skills/{skillId}/versions/{version}/publish", server.publishSkillVersion)
			admin.Get("/aep/v1/admin/skill-assignments", server.listSkillAssignments)
			admin.Post("/aep/v1/admin/skill-assignments", server.createSkillAssignment)
			admin.Delete("/aep/v1/admin/skill-assignments/{assignmentId}", server.deleteSkillAssignment)
			admin.Get("/aep/v1/admin/control-events", server.listAdminControlEvents)
			admin.Post("/aep/v1/admin/control-events", server.createControlEvent)
			admin.Get("/aep/v1/admin/control-events/{eventId}", server.getAdminControlEvent)
			admin.Post("/aep/v1/admin/control-events/{eventId}/cancel", server.cancelControlEvent)
			admin.Get("/aep/v1/admin/control-events/{eventId}/deliveries", server.listControlEventDeliveries)
			admin.Get("/aep/v1/admin/agents", server.listAgents)
			admin.Get("/aep/v1/admin/agents/{agentId}", server.getAgent)
			admin.Get("/aep/v1/admin/events", server.searchTelemetryEvents)
			admin.Get("/aep/v1/admin/models", server.listModels)
			admin.Post("/aep/v1/admin/models", server.createModel)
			admin.Get("/aep/v1/admin/models/{modelId}", server.getModel)
			admin.Patch("/aep/v1/admin/models/{modelId}", server.updateModel)
			admin.Delete("/aep/v1/admin/models/{modelId}", server.deleteModel)
			admin.Get("/aep/v1/admin/model-assignments", server.listModelAssignments)
			admin.Post("/aep/v1/admin/model-assignments", server.createModelAssignment)
			admin.Delete("/aep/v1/admin/model-assignments/{assignmentId}", server.deleteModelAssignment)
		})
	})
	server.router = router
	return server
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestID := request.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		response.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), contextKey("request-id"), requestID)))
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		header := request.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeProblem(response, request, http.StatusUnauthorized, "TOKEN_INVALID", "A bearer access token is required.")
			return
		}
		claims, err := s.app.Tokens.ParseAccess(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			writeProblem(response, request, http.StatusUnauthorized, "TOKEN_INVALID", "The access token is invalid or expired.")
			return
		}
		if agentID := request.Header.Get("X-AEP-Agent-ID"); agentID != "" && agentID != claims.AgentID {
			writeProblem(response, request, http.StatusConflict, "AGENT_IDENTITY_CONFLICT", "The Agent header does not match the authenticated session.")
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !claimsFrom(request).Admin {
			writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "Administrator access is required.")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func claimsFrom(request *http.Request) *auth.Claims {
	claims, _ := request.Context().Value(claimsContextKey).(*auth.Claims)
	if claims == nil {
		return &auth.Claims{}
	}
	return claims
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProblem(response, request, http.StatusBadRequest, "INVALID_REQUEST", "The JSON request body is invalid.")
		return false
	}
	return true
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("encode response failed", "error", err)
	}
}

func writeProblem(response http.ResponseWriter, request *http.Request, status int, code, detail string) {
	response.Header().Set("Content-Type", "application/problem+json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"type":  "https://aep.example/problems/" + strings.ToLower(strings.ReplaceAll(code, "_", "-")),
		"title": http.StatusText(status), "status": status, "detail": detail, "code": code,
		"requestId": request.Context().Value(contextKey("request-id")),
	})
}

func databaseFailure(response http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	slog.Error("database operation failed", "request_id", request.Context().Value(contextKey("request-id")), "error", err)
	writeProblem(response, request, http.StatusInternalServerError, "INTERNAL_ERROR", "The operation could not be completed.")
}

func limit(request *http.Request) int32 {
	value := int32(50)
	if parsed, err := json.Number(request.URL.Query().Get("limit")).Int64(); err == nil && parsed > 0 && parsed <= 200 {
		value = int32(parsed)
	}
	return value
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	if err := s.app.Pool.Ping(ctx); err != nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "PostgreSQL is unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getJWKS(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, s.app.Tokens.JWKS())
}

func (s *Server) metadata(response http.ResponseWriter, _ *http.Request) {
	capabilities := []string{"password_auth", "federated_auth", "skills", "telemetry", "control_events"}
	metadata := map[string]any{
		"service": "aep-control-service", "supportedProtocolVersions": []string{"1.0"},
		"capabilities": capabilities, "jwksUri": "/.well-known/jwks.json",
	}
	if s.app.Config.ModelGatewayBaseURL != "" {
		capabilities = append(capabilities, "model_gateway")
		metadata["capabilities"] = capabilities
		metadata["modelGateway"] = map[string]string{
			"baseUrl": s.app.Config.ModelGatewayBaseURL, "protocol": "openai-compatible", "apiVersion": "v1",
		}
	}
	writeJSON(response, http.StatusOK, metadata)
}
