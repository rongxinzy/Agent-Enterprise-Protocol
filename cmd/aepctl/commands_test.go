package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestManagementCommandsMapFlagsToRequests(t *testing.T) {
	tests := []struct {
		name, method, path string
		args               []string
		check              func(*testing.T, map[string]any)
	}{
		{
			name: "model create", method: http.MethodPost, path: "/aep/v1/admin/models",
			args: []string{"model", "create", "--model-id", "chat-a", "--display-name", "Chat A", "--credential-id", "credential-a", "--reasoning-format", "deepseek", "--context-window", "32768"},
			check: func(t *testing.T, body map[string]any) {
				reasoning, ok := body["reasoningCompatibility"].(map[string]any)
				if !ok || reasoning["thinkingFormat"] != "deepseek" || reasoning["requiresReasoningContentOnAssistantMessages"] != true || body["credentialId"] != "credential-a" || body["contextWindow"] != float64(32768) {
					t.Errorf("model flags were not mapped to the create payload")
				}
			},
		},
		{
			name: "model update clears optional fields", method: http.MethodPatch, path: "/aep/v1/admin/models/chat-a",
			args: []string{"model", "update", "--model-id", "chat-a", "--credential-id", "", "--reasoning-format", "", "--enabled=false"},
			check: func(t *testing.T, body map[string]any) {
				if len(body) != 3 || body["credentialId"] != nil || body["reasoningCompatibility"] != nil || body["enabled"] != false {
					t.Errorf("model update did not clear optional fields")
				}
			},
		},
		{
			name: "credential update only changed field", method: http.MethodPatch, path: "/aep/v1/admin/credentials/credential-a",
			args: []string{"credential", "update", "--credential-id", "credential-a", "--enabled=false"},
			check: func(t *testing.T, body map[string]any) {
				if len(body) != 1 || body["enabled"] != false {
					t.Errorf("credential update included unchanged fields")
				}
			},
		},
		{
			name: "skill role assignment", method: http.MethodPost, path: "/aep/v1/admin/skill-assignments",
			args: []string{"skill", "assign", "--skill-id", "skill-a", "--subject-type", "role", "--subject-id", "role-a"},
			check: func(t *testing.T, body map[string]any) {
				subject, ok := body["subject"].(map[string]any)
				if !ok || body["skillId"] != "skill-a" || subject["type"] != "role" || subject["id"] != "role-a" {
					t.Errorf("skill assignment subject was not mapped")
				}
			},
		},
		{
			name: "control event user scope", method: http.MethodPost, path: "/aep/v1/admin/control-events",
			args: []string{"event", "publish", "--scope-type", "user", "--scope-id", "user-a", "--skill-id", "skill-a"},
			check: func(t *testing.T, body map[string]any) {
				scope, scopeOK := body["scope"].(map[string]any)
				resource, resourceOK := body["resource"].(map[string]any)
				if !scopeOK || !resourceOK || scope["id"] != "user-a" || resource["id"] != "skill-a" || body["supersedesKey"] != "skill:skill-a:user:user-a" {
					t.Errorf("event scope and resource were not mapped")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/aep/v1/auth/password/login" {
					_, _ = io.WriteString(w, `{"accessToken":"access-a"}`)
					return
				}
				if r.Method != test.method || r.URL.Path != test.path || r.Header.Get("Authorization") != "Bearer access-a" {
					t.Errorf("unexpected management request: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode management request: %v", err)
				} else {
					test.check(t, body)
				}
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			command := newRootCommand()
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			args := []string{"--base-url", server.URL, "--password", "test-only"}
			command.SetArgs(append(args, test.args...))
			if err := command.Execute(); err != nil || calls != 2 {
				t.Fatalf("command calls = %d, error = %v", calls, err)
			}
		})
	}
}

func TestCredentialValueRequiresExactlyOneSource(t *testing.T) {
	newCommand := func() *cobra.Command {
		command := &cobra.Command{}
		command.Flags().String("value", "", "")
		command.Flags().String("value-file", "", "")
		return command
	}
	t.Setenv("AEPCTL_CREDENTIAL_VALUE", "")
	if _, err := credentialValue(newCommand(), "", ""); err == nil {
		t.Fatal("credentialValue accepted no source")
	}
	command := newCommand()
	if err := command.Flags().Set("value", "test-value"); err != nil {
		t.Fatal(err)
	}
	if value, err := credentialValue(command, "test-value", ""); err != nil || value != "test-value" {
		t.Fatalf("flag source = %q, %v", value, err)
	}
	t.Setenv("AEPCTL_CREDENTIAL_VALUE", "environment-value")
	if _, err := credentialValue(command, "test-value", ""); err == nil {
		t.Fatal("credentialValue accepted multiple sources")
	}
	if value, err := credentialValue(newCommand(), "", ""); err != nil || value != "environment-value" {
		t.Fatalf("environment source = %q, %v", value, err)
	}
	t.Setenv("AEPCTL_CREDENTIAL_VALUE", "")
	path := filepath.Join(t.TempDir(), "credential.txt")
	if err := os.WriteFile(path, []byte("file-value\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command = newCommand()
	if err := command.Flags().Set("value-file", path); err != nil {
		t.Fatal(err)
	}
	if value, err := credentialValue(command, "", path); err != nil || value != "file-value" {
		t.Fatalf("file source = %q, %v", value, err)
	}
	if _, err := credentialValue(command, "", path+".missing"); !os.IsNotExist(err) {
		t.Fatalf("missing source file error = %v", err)
	}
}
