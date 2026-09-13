package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginUsesProtocolAndAcceptsOnlyAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/aep/v1/auth/password/login" || r.Header.Get("X-AEP-Protocol-Version") != "1.0" || r.Header.Get("X-Request-ID") == "" {
			t.Errorf("unexpected login request: %s %s %#v", r.Method, r.URL.Path, r.Header)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("login must send JSON without bearer credentials")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["deploymentId"] != "deployment-a" || body["username"] != "admin" || body["password"] != "secret" {
			t.Errorf("unexpected login payload: %v", err)
		}
		_, _ = io.WriteString(w, `{"accessToken":"access-a"}`)
	}))
	defer server.Close()
	api := &client{baseURL: server.URL, http: server.Client()}
	if err := api.login(&options{deploymentID: "deployment-a", username: "admin", password: "secret"}); err != nil || api.token != "access-a" {
		t.Fatalf("login() token = %q, error = %v", api.token, err)
	}

	for _, response := range []string{`[]`, `{}`, `{"accessToken":123}`} {
		t.Run(response, func(t *testing.T) {
			invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, response)
			}))
			defer invalid.Close()
			api := &client{baseURL: invalid.URL, http: invalid.Client()}
			if err := api.login(&options{}); err == nil || api.token != "" {
				t.Fatalf("invalid login response accepted: token = %q, error = %v", api.token, err)
			}
		})
	}
}

func TestRequestHeadersResponsesAndFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-AEP-Protocol-Version") != "1.0" || r.Header.Get("X-Request-ID") == "" {
			t.Error("request lacks protocol version or request ID")
		}
		switch r.URL.Path {
		case "/authenticated":
			if r.Header.Get("Authorization") != "Bearer access-a" || r.Header.Get("Content-Type") != "application/json" || r.Method != http.MethodPost {
				t.Error("authenticated JSON request has incorrect headers or method")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["name"] != "test" {
				t.Errorf("incorrect JSON request: %v", err)
			}
			_, _ = io.WriteString(w, `{"id":"item-a"}`)
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		case "/problem":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"FORBIDDEN"}`)
		case "/invalid":
			_, _ = io.WriteString(w, `not json`)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	api := &client{baseURL: server.URL, token: "access-a", http: server.Client()}
	value, err := api.request(http.MethodPost, "/authenticated", map[string]string{"name": "test"}, true)
	if err != nil || value.(map[string]any)["id"] != "item-a" {
		t.Fatalf("authenticated request = %#v, %v", value, err)
	}
	if value, err := api.request(http.MethodGet, "/empty", nil, false); err != nil || value != nil {
		t.Fatalf("empty response = %#v, %v", value, err)
	}
	if _, err := api.request(http.MethodGet, "/problem", nil, false); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("HTTP failure = %v", err)
	}
	if _, err := api.request(http.MethodGet, "/invalid", nil, false); err == nil {
		t.Fatal("malformed JSON response accepted")
	}
	api.baseURL = ":"
	if _, err := api.request(http.MethodGet, "/invalid", nil, false); err == nil {
		t.Fatal("invalid base URL accepted")
	}
	api.baseURL = server.URL
	if _, err := api.request(http.MethodPost, "/authenticated", make(chan int), true); err == nil {
		t.Fatal("unserializable body accepted")
	}
}

func TestUploadSkillMultipartAndFailure(t *testing.T) {
	packagePath := filepath.Join(t.TempDir(), "skill.zip")
	if err := os.WriteFile(packagePath, []byte("zip payload"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/aep/v1/admin/skills/skill%2Ftest/versions" || r.Header.Get("Authorization") != "Bearer access-a" || r.Header.Get("X-AEP-Protocol-Version") != "1.0" {
			t.Errorf("unexpected upload request: %s %s", r.Method, r.URL.EscapedPath())
		}
		if err := r.ParseMultipartForm(1024); err != nil || r.FormValue("version") == "" {
			t.Errorf("invalid multipart form: %v", err)
		}
		file, header, err := r.FormFile("package")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil || header.Filename != "skill.zip" || string(content) != "zip payload" {
			t.Errorf("unexpected uploaded package: %v", err)
		}
		if r.FormValue("version") == "denied" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"FORBIDDEN"}`)
			return
		}
		if r.FormValue("version") == "invalid" {
			_, _ = io.WriteString(w, `not json`)
			return
		}
		_, _ = io.WriteString(w, `{"version":"1.0"}`)
	}))
	defer server.Close()
	api := &client{baseURL: server.URL, token: "access-a", http: server.Client()}
	value, err := api.uploadSkill("skill/test", "1.0", packagePath)
	if err != nil || value.(map[string]any)["version"] != "1.0" {
		t.Fatalf("uploadSkill() = %#v, %v", value, err)
	}
	if _, err := api.uploadSkill("skill/test", "1.0", packagePath+".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing package error = %v", err)
	}
	if _, err := api.uploadSkill("skill/test", "denied", packagePath); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("upload HTTP failure = %v", err)
	}
	if _, err := api.uploadSkill("skill/test", "invalid", packagePath); err == nil {
		t.Fatal("upload accepted malformed JSON response")
	}
}

func TestAuthenticatedCommandRequiresPasswordBeforeRequest(t *testing.T) {
	command := newRootCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--password", "", "user", "list"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "administrator password is required") {
		t.Fatalf("user list without password = %v", err)
	}
}

func TestUserCreateCommandLogsInAndCallsManagementAPI(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/aep/v1/auth/password/login":
			if requests != 1 || r.Header.Get("Authorization") != "" {
				t.Error("login must precede management request")
			}
			_, _ = io.WriteString(w, `{"accessToken":"access-a"}`)
		case "/aep/v1/admin/users":
			if requests != 2 || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer access-a" {
				t.Error("user creation lacks login bearer token")
			}
			var body struct {
				DeploymentID          string   `json:"deploymentId"`
				Username              string   `json:"username"`
				DisplayName           string   `json:"displayName"`
				TeamIDs               []string `json:"teamIds"`
				RoleIDs               []string `json:"roleIds"`
				RequirePasswordChange bool     `json:"requirePasswordChange"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeploymentID != "deployment-a" || body.Username != "member-a" || body.DisplayName != "Member A" || len(body.TeamIDs) != 1 || body.TeamIDs[0] != "team-a" || len(body.RoleIDs) != 1 || body.RoleIDs[0] != "role-a" || !body.RequirePasswordChange {
				t.Errorf("unexpected user creation body: %#v, %v", body, err)
			}
			_, _ = io.WriteString(w, `{"id":"member-a"}`)
		default:
			t.Errorf("unexpected API path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	command := newRootCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--base-url", server.URL, "--deployment", "deployment-a", "--username", "admin", "--password", "secret", "user", "create", "--user", "member-a", "--display-name", "Member A", "--temporary-password", "temporary", "--team-id", "team-a", "--role-id", "role-a"})
	if err := command.Execute(); err != nil || requests != 2 {
		t.Fatalf("user create command: requests = %d, error = %v", requests, err)
	}
}
