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
	supportedProtocolVersion string     = "1.0"
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

// New mounts every API surface on one router — the all-in-one deployment
// shape used by compose, local k3s, and tests. Split deployments construct
// NewEnterpriseAPI or NewAgentControlAPI instead.
func New(application *app.App, runtimeMiddleware ...func(http.Handler) http.Handler) *Server {
	server := newServer(application, runtimeMiddleware)
	server.mountCommon(server.router)
	server.mountAuth(server.router)
	server.mountAgentUser(server.router)
	server.mountInternal(server.router)
	server.mountAdmin(server.router)
	return server
}

// NewEnterpriseAPI mounts the enterprise management surface: admin APIs,
// service-to-service internal endpoints, auth (sessions are shared across
// surfaces — the console and portal log in here), and discovery metadata.
func NewEnterpriseAPI(application *app.App, runtimeMiddleware ...func(http.Handler) http.Handler) *Server {
	server := newServer(application, runtimeMiddleware)
	server.mountCommon(server.router)
	server.mountAuth(server.router)
	server.mountInternal(server.router)
	server.mountAdmin(server.router)
	return server
}

// NewAgentControlAPI mounts the agent control protocol: the desktop-agent
// runtime surface (/aep/v1/user/*), session auth, and discovery metadata.
// The AEP participant model (docs/aep-v1.md) names this the Control, Event,
// and Asset service; this binary actualizes it as its own process.
func NewAgentControlAPI(application *app.App, runtimeMiddleware ...func(http.Handler) http.Handler) *Server {
	server := newServer(application, runtimeMiddleware)
	server.mountCommon(server.router)
	server.mountAuth(server.router)
	server.mountAgentUser(server.router)
	return server
}

func newServer(application *app.App, runtimeMiddleware []func(http.Handler) http.Handler) *Server {
	server := &Server{app: application, transactions: make(map[string]federatedTransaction)}
	router := chi.NewRouter()
	router.Use(server.requestID)
	for _, use := range runtimeMiddleware {
		router.Use(use)
	}
	router.Use(server.protocolVersion)
	router.Use(middleware.Recoverer)
	server.router = router
	return server
}

// mountCommon registers the discovery and health surface shared by every
// API shape: JWKS (the only public verification surface), service metadata,
// and probes.
func (s *Server) mountCommon(router chi.Router) {
	router.Get("/.well-known/jwks.json", s.getJWKS)
	router.Get("/livez", s.liveness)
	router.Get("/readyz", s.readiness)
	router.Get("/healthz", s.readiness)
	router.Get("/aep/v1/metadata", s.metadata)
}

// mountAuth registers the session surface. Access tokens and refresh tokens
// are validated against the shared database and signing key, so both the
// enterprise and agent surfaces can serve login and refresh interchangeably.
func (s *Server) mountAuth(router chi.Router) {
	router.Get("/aep/v1/auth/methods", s.authenticationMethods)
	router.Post("/aep/v1/auth/password/login", s.passwordLogin)
	router.Post("/aep/v1/auth/federated/start", s.federatedStart)
	router.Post("/aep/v1/auth/exchange", s.federatedExchange)
	router.Post("/aep/v1/auth/refresh", s.refreshSession)
	router.Group(func(protected chi.Router) {
		protected.Use(s.authenticate)
		protected.Post("/aep/v1/auth/password/change", s.changePassword)
		protected.Post("/aep/v1/auth/logout", s.logout)
	})
}

// mountAgentUser registers the agent control protocol runtime surface:
// heartbeat, control-event inbox, skill delivery, telemetry, model
// connection, and credential resolution for the authenticated agent.
func (s *Server) mountAgentUser(router chi.Router) {
	router.Group(func(protected chi.Router) {
		protected.Use(s.authenticate)
		protected.Post("/aep/v1/user/activation", s.activateLicense)
		protected.Get("/aep/v1/user/me", s.currentIdentity)
		protected.Get("/aep/v1/user/models", s.listAgentModels)
		protected.Get("/aep/v1/user/credentials", s.listAgentCredentials)
		protected.Post("/aep/v1/user/credentials/{credentialId}/resolve", s.resolveAgentCredential)
		protected.Post("/aep/v1/user/heartbeat", s.heartbeat)
		protected.Get("/aep/v1/user/control-events", s.listAgentControlEvents)
		protected.Post("/aep/v1/user/control-events/{deliveryId}/acknowledge", s.acknowledgeControlEvent)
		protected.Post("/aep/v1/user/control-events/{deliveryId}/result", s.reportControlEventResult)
		protected.Get("/aep/v1/user/skills/manifest", s.skillManifest)
		protected.Get("/aep/v1/user/skills/{skillId}/versions/{version}/package", s.downloadSkillPackage)
		protected.Post("/aep/v1/user/skills/sync-results", s.reportSkillSyncResult)
		protected.Post("/aep/v1/user/events/batch", s.uploadTelemetryBatch)
	})
}

// mountInternal registers the service-to-service endpoints consumed by the
// gateway data plane (reconciler desired-state sync, authorizer license
// status) behind their static shared secrets.
func (s *Server) mountInternal(router chi.Router) {
	router.Get("/internal/data-plane/desired-state", s.internalDataPlane(s.getDataPlaneDesiredState))
	router.Put("/internal/data-plane/status", s.internalDataPlane(s.putInternalDataPlaneStatus))
	router.Get("/internal/gateway/licenses/{licenseId}", s.internalLicenseStatus)
}

// mountAdmin registers the enterprise management API behind session auth
// plus the admin permission check.
func (s *Server) mountAdmin(router chi.Router) {
	router.Group(func(protected chi.Router) {
		protected.Use(s.authenticate)
		protected.Group(func(admin chi.Router) {
			admin.Use(s.requireAdmin)
			admin.Get("/aep/v1/admin/permissions", s.listPermissions)
			admin.Get("/aep/v1/admin/roles", s.listRoles)
			admin.Post("/aep/v1/admin/roles", s.createRole)
			admin.Get("/aep/v1/admin/roles/{roleId}", s.getRole)
			admin.Patch("/aep/v1/admin/roles/{roleId}", s.updateRole)
			admin.Delete("/aep/v1/admin/roles/{roleId}", s.deleteRole)
			admin.Get("/aep/v1/admin/teams", s.listTeams)
			admin.Post("/aep/v1/admin/teams", s.createTeam)
			admin.Get("/aep/v1/admin/teams/{teamId}", s.getTeam)
			admin.Patch("/aep/v1/admin/teams/{teamId}", s.updateTeam)
			admin.Delete("/aep/v1/admin/teams/{teamId}", s.deleteTeam)
			admin.Put("/aep/v1/admin/users/{userId}/rbac", s.replaceUserRBAC)
			admin.Get("/aep/v1/admin/users", s.listUsers)
			admin.Post("/aep/v1/admin/users", s.createUser)
			admin.Post("/aep/v1/admin/users/import", s.importUsers)
			admin.Get("/aep/v1/admin/users/{userId}", s.getUser)
			admin.Delete("/aep/v1/admin/users/{userId}", s.deleteUser)
			admin.Patch("/aep/v1/admin/users/{userId}", s.updateUser)
			admin.Post("/aep/v1/admin/users/{userId}/reset-password", s.resetUserPassword)
			admin.Get("/aep/v1/admin/skills", s.listSkills)
			admin.Post("/aep/v1/admin/skills", s.createSkill)
			admin.Get("/aep/v1/admin/skills/{skillId}", s.getSkill)
			admin.Patch("/aep/v1/admin/skills/{skillId}", s.updateSkill)
			admin.Delete("/aep/v1/admin/skills/{skillId}", s.deleteSkill)
			admin.Post("/aep/v1/admin/skills/{skillId}/versions", s.uploadSkillVersion)
			admin.Post("/aep/v1/admin/skills/{skillId}/versions/{version}/publish", s.publishSkillVersion)
			admin.Delete("/aep/v1/admin/skills/{skillId}/versions/{version}", s.deleteSkillVersion)
			admin.Get("/aep/v1/admin/skill-assignments", s.listSkillAssignments)
			admin.Post("/aep/v1/admin/skill-assignments", s.createSkillAssignment)
			admin.Delete("/aep/v1/admin/skill-assignments/{assignmentId}", s.deleteSkillAssignment)
			admin.Get("/aep/v1/admin/control-events", s.listAdminControlEvents)
			admin.Post("/aep/v1/admin/control-events", s.createControlEvent)
			admin.Get("/aep/v1/admin/control-events/{eventId}", s.getAdminControlEvent)
			admin.Post("/aep/v1/admin/control-events/{eventId}/cancel", s.cancelControlEvent)
			admin.Get("/aep/v1/admin/control-events/{eventId}/deliveries", s.listControlEventDeliveries)
			admin.Get("/aep/v1/admin/sessions", s.listUserSessions)
			admin.Post("/aep/v1/admin/sessions/{sessionId}/revoke", s.revokeUserSession)
			admin.Get("/aep/v1/admin/licenses", s.listLicenses)
			admin.Get("/aep/v1/admin/licenses/{licenseId}", s.getLicense)
			admin.Post("/aep/v1/admin/licenses/import", s.importLicense)
			admin.Post("/aep/v1/admin/licenses/{licenseId}/revoke", s.revokeLicense)
			admin.Get("/aep/v1/admin/events", s.searchTelemetryEvents)
			admin.Get("/aep/v1/admin/models", s.listModels)
			admin.Post("/aep/v1/admin/models", s.createModel)
			admin.Get("/aep/v1/admin/models/{modelId}", s.getModel)
			admin.Patch("/aep/v1/admin/models/{modelId}", s.updateModel)
			admin.Delete("/aep/v1/admin/models/{modelId}", s.deleteModel)
			admin.Get("/aep/v1/admin/model-assignments", s.listModelAssignments)
			admin.Post("/aep/v1/admin/model-assignments", s.createModelAssignment)
			admin.Delete("/aep/v1/admin/model-assignments/{assignmentId}", s.deleteModelAssignment)
			admin.Get("/aep/v1/admin/data-plane/desired-state", s.getDataPlaneDesiredState)
			admin.Put("/aep/v1/admin/data-plane/desired-state", s.putDataPlaneDesiredState)
			admin.Post("/aep/v1/admin/data-plane/publish", s.publishDataPlaneRoutes)
			admin.Get("/aep/v1/admin/data-plane/status", s.getDataPlaneStatus)
			admin.Get("/aep/v1/admin/deployment/settings", s.getDeploymentSettings)
			admin.Put("/aep/v1/admin/deployment/settings", s.updateDeploymentSettings)
			admin.Get("/aep/v1/admin/credentials", s.listCredentials)
			admin.Post("/aep/v1/admin/credentials", s.createCredential)
			admin.Get("/aep/v1/admin/credentials/{credentialId}", s.getCredential)
			admin.Patch("/aep/v1/admin/credentials/{credentialId}", s.updateCredential)
			admin.Delete("/aep/v1/admin/credentials/{credentialId}", s.deleteCredential)
			admin.Post("/aep/v1/admin/credentials/{credentialId}/rotate", s.rotateCredential)
			admin.Get("/aep/v1/admin/credential-assignments", s.listCredentialAssignments)
			admin.Post("/aep/v1/admin/credential-assignments", s.createCredentialAssignment)
			admin.Delete("/aep/v1/admin/credential-assignments/{assignmentId}", s.deleteCredentialAssignment)
			admin.Get("/aep/v1/admin/agents", s.listAgents)
			admin.Post("/aep/v1/admin/agents", s.createAgent)
			admin.Delete("/aep/v1/admin/agents/{agentId}", s.deleteAgent)
			admin.Put("/aep/v1/admin/agents/{agentId}/profile", s.updateAgentProfile)
			admin.Get("/aep/v1/admin/identity-sources", s.listIdentitySources)
			admin.Post("/aep/v1/admin/identity-sources", s.createIdentitySource)
			admin.Get("/aep/v1/admin/identity-sources/{sourceId}/mappings", s.listIdentityMappings)
			admin.Put("/aep/v1/admin/identity-sources/{sourceId}/mappings", s.upsertIdentityMapping)
			admin.Delete("/aep/v1/admin/identity-sources/{sourceId}/mappings/{subjectType}/{externalId}", s.deleteIdentityMapping)
			admin.Get("/aep/v1/admin/data-scope-rules", s.listDataScopeRules)
			admin.Post("/aep/v1/admin/data-scope-rules", s.createDataScopeRule)
			admin.Get("/aep/v1/admin/data-scope-rules/{ruleId}", s.getDataScopeRule)
			admin.Delete("/aep/v1/admin/data-scope-rules/{ruleId}", s.deleteDataScopeRule)
			admin.Get("/aep/v1/admin/data-scope/context", s.dataScopeContext)
		})
	})
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
		if claims.SessionID == "" {
			writeProblem(response, request, http.StatusUnauthorized, "SESSION_REQUIRED", "The access token is not bound to a user session.")
			return
		}
		state, err := s.app.ValidateAccessSession(request.Context(), claims.DeploymentID, claims.Subject, claims.SessionID)
		if errors.Is(err, app.ErrAccessSessionInvalid) {
			writeProblem(response, request, http.StatusUnauthorized, "SESSION_REVOKED", "The user session is inactive or revoked.")
			return
		}
		if err != nil {
			databaseFailure(response, request, err)
			return
		}
		// Authorization and password state are mutable and must never be trusted
		// from a previously issued access token.
		claims.Admin = state.Admin
		claims.PasswordChangeRequired = state.PasswordChangeRequired
		if state.PasswordChangeRequired && !passwordChangeRouteAllowed(request) {
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
		permissions := requiredAdminPermission(request.Method, request.URL.Path)
		if len(permissions) == 0 {
			writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "The authenticated user lacks the required management permission.")
			return
		}
		allowed := false
		for _, permission := range permissions {
			permitted, err := s.userHasPermission(request, permission)
			if err != nil {
				databaseFailure(response, request, err)
				return
			}
			if permitted {
				allowed = true
				break
			}
		}
		if !allowed {
			writeProblem(response, request, http.StatusForbidden, "ACCESS_DENIED", "The authenticated user lacks the required management permission.")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (s *Server) userHasPermission(request *http.Request, permission string) (bool, error) {
	database := s.app.Database()
	if database == nil {
		return false, errors.New("database unavailable")
	}
	var allowed bool
	err := database.QueryRow(request.Context(), `SELECT EXISTS (
  SELECT 1 FROM user_role_bindings urb
  JOIN roles r ON r.deployment_id=urb.deployment_id AND r.id=urb.role_id AND r.enabled=true
  JOIN role_permissions rp ON rp.deployment_id=urb.deployment_id AND rp.role_id=urb.role_id AND rp.permission_id=$3
  WHERE urb.deployment_id=$1 AND urb.user_id=$2
)`, claimsFrom(request).DeploymentID, claimsFrom(request).Subject, permission).Scan(&allowed)
	return allowed, err
}

func requiredAdminPermission(method, path string) []string {
	switch {
	case strings.HasPrefix(path, "/aep/v1/admin/permissions") || strings.HasPrefix(path, "/aep/v1/admin/roles"):
		if method == http.MethodGet {
			return []string{"roles.read"}
		}
		return []string{"roles.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/teams"):
		if method == http.MethodGet {
			return []string{"teams.read"}
		}
		return []string{"teams.write"}
	case strings.HasSuffix(path, "/rbac"):
		return []string{"users.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/users"):
		if method == http.MethodGet {
			return []string{"users.read"}
		}
		return []string{"users.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/sessions"):
		if strings.HasSuffix(path, "/revoke") {
			return []string{"sessions.write"}
		}
		return []string{"users.read"}
	case strings.HasPrefix(path, "/aep/v1/admin/models"):
		if strings.Contains(path, "assignment") {
			return []string{"models.assign"}
		}
		if method == http.MethodGet {
			return []string{"models.read"}
		}
		return []string{"models.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/skills"):
		if strings.Contains(path, "assignment") {
			return []string{"skills.assign"}
		}
		if method == http.MethodGet {
			return []string{"skills.read"}
		}
		return []string{"skills.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/credentials"):
		if strings.Contains(path, "assignment") {
			return []string{"credentials.assign"}
		}
		if method == http.MethodGet {
			return []string{"credentials.read"}
		}
		return []string{"credentials.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/licenses"):
		if strings.HasSuffix(path, "/revoke") {
			return []string{"licenses.revoke"}
		}
		if method != http.MethodGet {
			return []string{"licenses.write"}
		}
		return []string{"licenses.read"}
	case strings.HasPrefix(path, "/aep/v1/admin/events") || strings.HasPrefix(path, "/aep/v1/admin/control-events"):
		if method == http.MethodGet {
			return []string{"events.read"}
		}
		return []string{"events.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/data-plane"):
		return []string{"data_plane.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/deployment"):
		if method == http.MethodGet {
			return []string{"deployment.read"}
		}
		return []string{"deployment.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/agents"):
		if method == http.MethodGet {
			return []string{"users.read"}
		}
		// agents.write is the delegable digital-employee lifecycle grant;
		// users.write keeps working so administrators migrate transparently.
		return []string{"agents.write", "users.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/identity-sources"):
		if method == http.MethodGet {
			return []string{"identity.read"}
		}
		return []string{"identity.write"}
	case strings.HasPrefix(path, "/aep/v1/admin/data-scope"):
		if method == http.MethodGet {
			return []string{"data_scope.read"}
		}
		return []string{"data_scope.write"}
	}
	return nil
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

func (s *Server) metadata(response http.ResponseWriter, request *http.Request) {
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
	if baseURL := effectiveModelGatewayBaseURL(s.app, request); baseURL != "" {
		capabilities = append(capabilities, "model_gateway")
		metadata["capabilities"] = capabilities
		metadata["modelGateway"] = map[string]string{
			"baseUrl": baseURL, "protocol": "openai-compatible", "apiVersion": "v1",
		}
	}
	if s.app.Credentials != nil {
		capabilities = append(capabilities, "credentials")
		metadata["capabilities"] = capabilities
	}
	// Split deployments advertise the agent control protocol endpoint so
	// desktop agents can redirect their runtime surface (heartbeat, events,
	// skills) without configuration; all-in-one deployments omit it and
	// clients keep using the API base they logged in against.
	if baseURL := effectiveAgentControlBaseURL(s.app, request); baseURL != "" {
		metadata["agentControl"] = map[string]string{"baseUrl": baseURL}
	}
	writeJSON(response, http.StatusOK, metadata)
}

func mockFederatedAuthEnabled(cfg config.Config) bool {
	return cfg.EnableMockFederatedAuth && cfg.Environment != "production"
}
