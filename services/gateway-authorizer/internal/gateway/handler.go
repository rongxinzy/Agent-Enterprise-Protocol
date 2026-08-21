package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (*ModelClaims, error)
	Ready(ctx context.Context) error
}

type Handler struct {
	verifier TokenVerifier
	proxy    *httputil.ReverseProxy
	limit    int64
}

func NewHandler(config Config, verifier TokenVerifier) (*Handler, error) {
	upstream, err := url.Parse(config.UpstreamURL)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return nil, errors.New("AEP_GATEWAY_UPSTREAM_URL must be an absolute HTTP URL")
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(response http.ResponseWriter, request *http.Request, err error) {
		slog.Error("model gateway upstream failed", "request_id", requestID(request), "error", err)
		writeProblem(response, request, http.StatusBadGateway, "GATEWAY_UPSTREAM_UNAVAILABLE", "The model gateway upstream is unavailable.")
	}
	return &Handler{verifier: verifier, proxy: proxy, limit: config.RequestLimit}, nil
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	request = withRequestID(response, request)
	if request.URL.Path == "/livez" {
		writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if request.URL.Path == "/healthz" || request.URL.Path == "/readyz" {
		if err := h.verifier.Ready(request.Context()); err != nil {
			slog.Warn("gateway readiness check failed", "dependency", "jwks", "error", err)
			writeProblem(response, request, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service dependency is unavailable.")
			return
		}
		writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, "/v1/") {
		writeProblem(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The model gateway route was not found.")
		return
	}
	rawToken, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		response.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(response, request, http.StatusUnauthorized, "TOKEN_INVALID", "A model bearer token is required.")
		return
	}
	claims, err := h.verifier.Verify(request.Context(), rawToken)
	if err != nil {
		response.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(response, request, http.StatusUnauthorized, "TOKEN_INVALID", "The model token is invalid or expired.")
		return
	}
	body, model, err := readModelRequest(request.Body, h.limit)
	if err != nil {
		status := http.StatusBadRequest
		code := "INVALID_REQUEST"
		detail := "The inference request must be valid JSON with a non-empty model."
		if errors.Is(err, errRequestTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "REQUEST_TOO_LARGE"
			detail = "The inference request exceeds the configured size limit."
		}
		writeProblem(response, request, status, code, detail)
		return
	}
	if !contains(claims.ModelScopes, model) {
		writeProblem(response, request, http.StatusForbidden, "MODEL_NOT_ALLOWED", "The model token does not grant access to the requested model.")
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Del("Authorization")
	setTrustedHeader(request.Header, "X-AEP-Tenant-ID", claims.Tenant)
	setTrustedHeader(request.Header, "X-AEP-User-ID", claims.Subject)
	setTrustedHeader(request.Header, "X-AEP-Agent-ID", claims.AgentID)
	setTrustedHeader(request.Header, "X-AEP-Model-ID", model)
	h.proxy.ServeHTTP(response, request)
}

var errRequestTooLarge = errors.New("request body is too large")

func readModelRequest(reader io.Reader, limit int64) ([]byte, string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > limit {
		return nil, "", errRequestTooLarge
	}
	var input struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &input); err != nil || strings.TrimSpace(input.Model) == "" {
		return nil, "", errors.New("model is required")
	}
	return body, input.Model, nil
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	token = strings.TrimSpace(token)
	return token, found && strings.EqualFold(scheme, "Bearer") && token != ""
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func setTrustedHeader(header http.Header, name, value string) {
	header.Del(name)
	header.Set(name, value)
}

func withRequestID(response http.ResponseWriter, request *http.Request) *http.Request {
	id := request.Header.Get("X-Request-ID")
	if !validRequestID(id) {
		id = uuid.NewString()
	}
	request.Header.Set("X-Request-ID", id)
	response.Header().Set("X-Request-ID", id)
	return request
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

func requestID(request *http.Request) string {
	return request.Header.Get("X-Request-ID")
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeProblem(response http.ResponseWriter, request *http.Request, status int, code, detail string) {
	response.Header().Set("Content-Type", "application/problem+json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"type":  "https://aep.example/problems/" + strings.ToLower(strings.ReplaceAll(code, "_", "-")),
		"title": http.StatusText(status), "status": status, "detail": detail, "code": code,
		"requestId": requestID(request),
	})
}
