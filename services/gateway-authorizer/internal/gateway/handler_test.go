package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

type verifierStub struct {
	claims *ModelClaims
	err    error
}

func (v verifierStub) Verify(context.Context, string) (*ModelClaims, error) {
	return v.claims, v.err
}

func TestHandlerAuthorizesModelAndSanitizesHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			t.Error("client authorization reached the upstream")
		}
		if request.Header.Get("X-AEP-Tenant-ID") != "enterprise-a" || request.Header.Get("X-AEP-Agent-ID") != "agent-a" || request.Header.Get("X-AEP-User-ID") != "user-a" || request.Header.Get("X-AEP-Model-ID") != "model-a" {
			t.Errorf("unexpected trusted headers: %#v", request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != "{\"model\":\"model-a\",\"stream\":true}" {
			t.Errorf("request body changed: %s", body)
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("data: ok\n\n"))
	}))
	defer upstream.Close()
	handler, err := NewHandler(Config{UpstreamURL: upstream.URL, RequestLimit: 1024}, verifierStub{claims: &ModelClaims{
		Tenant: "enterprise-a", AgentID: "agent-a", ModelScopes: []string{"model-a"},
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{\"model\":\"model-a\",\"stream\":true}"))
	request.Header.Set("Authorization", "Bearer model-token")
	request.Header.Set("X-AEP-Tenant-ID", "attacker")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "data: ok\n\n" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsMissingTokenAndUnauthorizedModel(t *testing.T) {
	handler, err := NewHandler(Config{UpstreamURL: "http://example.test", RequestLimit: 1024}, verifierStub{claims: &ModelClaims{ModelScopes: []string{"model-a"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		token  string
		model  string
		status int
		code   string
	}{
		{name: "missing token", model: "model-a", status: http.StatusUnauthorized, code: "TOKEN_INVALID"},
		{name: "unauthorized model", token: "Bearer token", model: "model-b", status: http.StatusForbidden, code: "MODEL_NOT_ALLOWED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{\"model\":\""+test.model+"\"}"))
			request.Header.Set("Authorization", test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), "\"code\":\""+test.code+"\"") {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHandlerRejectsOversizedAndInvalidRequests(t *testing.T) {
	handler, err := NewHandler(Config{UpstreamURL: "http://example.test", RequestLimit: 16}, verifierStub{claims: &ModelClaims{ModelScopes: []string{"model-a"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		body   string
		status int
	}{
		{body: "{}", status: http.StatusBadRequest},
		{body: "{\"model\":\"model-a\",\"padding\":\"too-large\"}", status: http.StatusRequestEntityTooLarge},
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("expected %d, got %d: %s", test.status, response.Code, response.Body.String())
		}
	}
}
