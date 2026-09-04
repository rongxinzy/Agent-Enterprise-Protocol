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
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
)

type contextKey string

const (
	claimsContextKey         contextKey = "aep-claims"
	supportedProtocolVersion            = "1.0"
)

type federatedTransaction struct {
	DeploymentID string
	State        string
	ExpiresAt    time.Time
}

type Server struct {
	app           *app.App
	router        chi.Router
	transactions  map[string]federatedTransaction
	transactionMu sync.Mutex
}

func New(application *app.App, runtimeMiddleware ...func(http.Handler) http.Handler) *Server {
	server := &Server{app: application, transactions: make(map[string]federatedTransaction)}
	router := chi.NewRouter()
	router.Use(server.requestID)
	for _, use := range runtimeMiddleware {
		router.Use(use)
	}
	router.Use(server.protocolVersion)
	router.Use(middleware.Recoverer)
	router.Get("/.well-known/jwks.json", server.getJWKS)
	router.Get("/livez", server.liveness)
	router.Get("/readyz", server.readiness)
	router.Get("/healthz", server.readiness)
	router.Get("/aep/v1/metadata", server.metadata)
	router.Get("/internal/data-plane/desired-state", server.internalDataPlane(server.getDataPlaneDesiredState))
	router.Put("/internal/data-plane/status", server.internalDataPlane(server.putInternalDataPlaneStatus))
	router.Get("/internal/gateway/licenses/{licenseId}", server.internalLicenseStatus)
	router.Get("/aep/v1/auth/methods", server.authenticationMethods)
	router.Post("/aep/v1/auth/password/login", server.passwordLogin)
	router.Post("/aep/v1/auth/federated/start", server.federatedStart)
	router.Post("/aep/v1/auth/exchange", server.federatedExchange)
	router.Post("/aep/v1/auth/refresh", server.refreshSession)

	router.Group(func(protected chi.Router) {
		protected.Use(server.authenticate)
		protected.Post("/aep/v1/auth/password/change", server.changePassword)
		protected.Post("/aep/v1/auth/logout", server.logout)
		protected.Post("/aep/v1/user/activation", server.activateLicense)
		protected.Get("/aep/v1/user/me", server.currentIdentity)
		protected.Get("/aep/v1/user/models", server.listAgentModels)
		protected.Get("/aep/v1/user/credentials", server.listAgentCredentials)
		protected.Post("/aep/v1/user/credentials/{credentialId}/resolve", server.resolveAgentCredential)
		protected.Post("/aep/v1/user/heartbeat", server.heartbeat)
		protected.Get("/aep/v1/user/control-events", server.listAgentControlEvents)
		protected.Post("/aep/v1/user/control-events/{deliveryId}/acknowledge", server.acknowledgeControlEvent)
		protected.Post("/aep/v1/user/control-events/{deliveryId}/result", server.reportControlEventResult)
		protected.Get("/aep/v1/user/skills/manifest", server.skillManifest)
		protected.Get("/aep/v1/user/skills/{skillId}/versions/{version}/package", server.downloadSkillPackage)
		protected.Post("/aep/v1/user/skills/sync-results", server.reportSkillSyncResult)
		protected.Post("/aep/v1/user/events/batch", server.uploadTelemetryBatch)

		protected.Group(func(admin chi.Router) {
			admin.Use(server.requireAdmin)
			admin.Get("/aep/v1/admin/permissions", server.listPermissions)
			admin.Get("/aep/v1/admin/roles", server.listRoles)
			admin.Post("/aep/v1/admin/roles", server.createRole)
			admin.Get("/aep/v1/admin/teams", server.listTeams)
			admin.Post("/aep/v1/admin/teams", server.createTeam)
			admin.Put("/aep/v1/admin/users/{userId}/rbac", server.replaceUserRBAC)
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
			admin.Get("/aep/v1/admin/sessions", server.listUserSessions)
			admin.Get("/aep/v1/admin/licenses", server.listLicenses)
			admin.Get("/aep/v1/admin/licenses/{licenseId}", server.getLicense)
			admin.Post("/aep/v1/admin/licenses/import", server.importLicense)
			admin.Post("/aep/v1/admin/licenses/{licenseId}/revoke", server.revokeLicense)
			admin.Get("/aep/v1/admin/events", server.searchTelemetryEvents)
			admin.Get("/aep/v1/admin/models", server.listModels)
			admin.Post("/aep/v1/admin/models", server.createModel)
			admin.Get("/aep/v1/admin/models/{modelId}", server.getModel)
			admin.Patch("/aep/v1/admin/models/{modelId}", server.updateModel)
			admin.Delete("/aep/v1/admin/models/{modelId}", server.deleteModel)
			admin.Get("/aep/v1/admin/model-assignments", server.listModelAssignments)
			admin.Post("/aep/v1/admin/model-assignments", server.createModelAssignment)
			admin.Delete("/aep/v1/admin/model-assignments/{assignmentId}", server.deleteModelAssignment)
			admin.Get("/aep/v1/admin/data-plane/desired-state", server.getDataPlaneDesiredState)
			admin.Put("/aep/v1/admin/data-plane/desired-state", server.putDataPlaneDesiredState)
			admin.Get("/aep/v1/admin/data-plane/status", server.getDataPlaneStatus)
			admin.Get("/aep/v1/admin/credentials", server.listCredentials)
			admin.Post("/aep/v1/admin/credentials", server.createCredential)
			admin.Get("/aep/v1/admin/credentials/{credentialId}", server.getCredential)
			admin.Patch("/aep/v1/admin/credentials/{credentialId}", server.updateCredential)
			admin.Delete("/aep/v1/admin/credentials/{credentialId}", server.deleteCredential)
			admin.Post("/aep/v1/admin/credentials/{credentialId}/rotate", server.rotateCredential)
			admin.Get("/aep/v1/admin/credential-assignments", server.listCredentialAssignments)
			admin.Post("/aep/v1/admin/credential-assignments", server.createCredentialAssignment)
			admin.Delete("/aep/v1/admin/credential-assignments/{assignmentId}", server.deleteCredentialAssignment)
		})
	})
	server.router = router
	return server
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestID := request.Header.Get("X-Request-ID")
		if !validRequestID(requestID) {
			requestID = uuid.NewString()
		}
		response.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), contextKey("request-id"), requestID)))
	})
}

func (s *Server) protocolVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/aep/v1/metadata" || !strings.HasPrefix(request.URL.Path, "/aep/v1/") {
			next.ServeHTTP(response, request)
			return
		}
		if request.Header.Get("X-AEP-Protocol-Version") != supportedProtocolVersion {
			response.Header().Set("X-AEP-Supported-Protocol-Versions", supportedProtocolVersion)
			writeProblem(response, request, http.StatusUpgradeRequired, "PROTOCOL_VERSION_UNSUPPORTED", "The AEP protocol version is missing or unsupported.")
			return
		}
		next.ServeHTTP(response, request)
	})
}
func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			!strings.ContainsRune("-_.:", character) {
			return false
		}
	}
	return true
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
		if claims.PasswordChangeRequired && !passwordChangeRouteAllowed(request) {
			writeProblem(response, request, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "The temporary password must be changed before using this operation.")
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims)))
	})
}

func passwordChangeRouteAllowed(request *http.Request) bool {
	if request.Method == http.MethodPost && (request.URL.Path == "/aep/v1/auth/password/change" || request.URL.Path == "/aep/v1/auth/logout") {
		return true
	}
	return request.Method == http.MethodGet && request.URL.Path == "/aep/v1/user/me"
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		claims := claimsFrom(request)
		if claims.Admin {
			next.ServeHTTP(response, request)
			return
		}
		permission := requiredAdminPermission(request.Method, request.URL.Path)
		if permission == "" {
			writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "The authenticated user lacks the required management permission.")
			return
		}
		allowed, err := s.userHasPermission(request, permission)
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		if !allowed {
			writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "The authenticated user lacks the required management permission.")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (s *Server) userHasPermission(request *http.Request, permission string) (bool, error) {
	var allowed bool
	err := s.app.Pool.QueryRow(request.Context(), `SELECT EXISTS (
  SELECT 1 FROM user_role_bindings urb
  JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true
  JOIN role_permissions rp ON rp.deployment_id=urb.deployment_id AND rp.role_id=urb.role_id AND rp.permission_id=$3
  WHERE urb.deployment_id=$1 AND urb.user_id=$2
)`, claimsFrom(request).Tenant, claimsFrom(request).Subject, permission).Scan(&allowed)
	return allowed, err
}

func requiredAdminPermission(method, path string) string {
	switch {
	case strings.HasPrefix(path, "/aep/v1/admin/permissions") || strings.HasPrefix(path, "/aep/v1/admin/roles"):
		if method == http.MethodGet {
			return "roles.read"
		}
		return "roles.write"
	case strings.HasPrefix(path, "/aep/v1/admin/teams"):
		if method == http.MethodGet {
			return "teams.read"
		}
		return "teams.write"
	case strings.HasSuffix(path, "/rbac"):
		return "users.write"
	case strings.HasPrefix(path, "/aep/v1/admin/users"):
		if method == http.MethodGet {
			return "users.read"
		}
		return "users.write"
	case strings.HasPrefix(path, "/aep/v1/admin/sessions"):
		return "users.read"
	case strings.HasPrefix(path, "/aep/v1/admin/models"):
		if strings.Contains(path, "assignment") {
			return "models.assign"
		}
		if method == http.MethodGet {
			return "models.read"
		}
		return "models.write"
	case strings.HasPrefix(path, "/aep/v1/admin/skills"):
		if strings.Contains(path, "assignment") {
			return "skills.assign"
		}
		if method == http.MethodGet {
			return "skills.read"
		}
		return "skills.write"
	case strings.HasPrefix(path, "/aep/v1/admin/credentials"):
		if strings.Contains(path, "assignment") {
			return "credentials.assign"
		}
		if method == http.MethodGet {
			return "credentials.read"
		}
		return "credentials.write"
	case strings.HasPrefix(path, "/aep/v1/admin/licenses"):
		if strings.HasSuffix(path, "/revoke") {
			return "licenses.revoke"
		}
		return "licenses.read"
	case strings.HasPrefix(path, "/aep/v1/admin/events") || strings.HasPrefix(path, "/aep/v1/admin/control-events"):
		if method == http.MethodGet {
			return "events.read"
		}
		return "events.write"
	case strings.HasPrefix(path, "/aep/v1/admin/data-plane"):
		return "data_plane.write"
	}
	return ""
}

func (s *Server) requirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			claims := claimsFrom(request)
			if claims.Admin {
				next.ServeHTTP(response, request)
				return
			}
			allowed, err := s.userHasPermission(request, permission)
			if err != nil {
				databaseFailure(response, request, err)
				return
			}
			if !allowed {
				writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "The authenticated user lacks the required permission.")
				return
			}
			next.ServeHTTP(response, request)
		})
	}
}

func (s *Server) internalDataPlane(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if s.app.Config.DataPlaneReconcilerToken == "" || request.Header.Get("X-AEP-Data-Plane-Token") != s.app.Config.DataPlaneReconcilerToken {
			writeProblem(response, request, http.StatusUnauthorized, "INTERNAL_AUTH_REQUIRED", "A valid data-plane service token is required.")
			return
		}
		deploymentID := strings.TrimSpace(request.Header.Get("X-AEP-Deployment-ID"))
		if deploymentID == "" || len(deploymentID) > 200 {
			writeProblem(response, request, http.StatusBadRequest, "DEPLOYMENT_REQUIRED", "The deployment header is required.")
			return
		}
		claims := &auth.Claims{DeploymentID: deploymentID}
		next(response, request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims)))
	}
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

func (s *Server) liveness(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readiness(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if s.app.Pool == nil || s.app.Blobs == nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service dependency is unavailable.")
		return
	}
	if err := s.app.Pool.Ping(ctx); err != nil {
		slog.Warn("readiness check failed", "dependency", "postgres", "error", err)
		writeProblem(response, request, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service dependency is unavailable.")
		return
	}
	if err := s.app.Blobs.Ready(ctx); err != nil {
		slog.Warn("readiness check failed", "dependency", "minio", "error", err)
		writeProblem(response, request, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service dependency is unavailable.")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getJWKS(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, s.app.Tokens.JWKS())
}

func (s *Server) metadata(response http.ResponseWriter, _ *http.Request) {
	capabilities := []string{"password_auth"}
	if mockFederatedAuthEnabled(s.app.Config) {
		capabilities = append(capabilities, "federated_auth")
	}
	capabilities = append(capabilities, "skills", "telemetry", "control_events")
	metadata := map[string]any{
		"service": "aep-control-service", "supportedProtocolVersions": []string{"1.0"},
		"capabilities": capabilities, "jwksUri": "/.well-known/jwks.json",
		"deploymentId": s.app.DeploymentID(),
		"deployment":   map[string]string{"id": s.app.DeploymentID(), "name": s.app.DeploymentName()},
	}
	if s.app.Config.ModelGatewayBaseURL != "" {
		capabilities = append(capabilities, "model_gateway")
		metadata["capabilities"] = capabilities
		metadata["modelGateway"] = map[string]string{
			"baseUrl": s.app.Config.ModelGatewayBaseURL, "protocol": "openai-compatible", "apiVersion": "v1",
		}
	}
	if s.app.Credentials != nil {
		capabilities = append(capabilities, "credentials")
		metadata["capabilities"] = capabilities
	}
	writeJSON(response, http.StatusOK, metadata)
}

func mockFederatedAuthEnabled(cfg config.Config) bool {
	return cfg.EnableMockFederatedAuth && cfg.Environment != "production"
}
