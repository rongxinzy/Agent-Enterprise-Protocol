package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicationCommandsUseManagementAPIAndExplicitKeys(t *testing.T) {
	const id = "81e147fc-c3b9-49b0-8ecf-0a88386734dc"
	for _, mode := range []string{"queue", "get", "replay"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/aep/v1/auth/password/login" {
					if r.Header.Get("Idempotency-Key") != "" {
						t.Error("publication key leaked to login")
					}
					_, _ = io.WriteString(w, `{"accessToken":"disposable-cli-token"}`)
					return
				}
				calls++
				if r.Header.Get("Authorization") != "Bearer disposable-cli-token" || r.Header.Get("X-AEP-Protocol-Version") != "1.0" {
					t.Error("missing authenticated protocol headers")
				}
				path := "/aep/v1/admin/data-plane/publications"
				if mode != "queue" {
					path += "/" + id
				}
				if mode == "replay" {
					path += "/replay"
				}
				if r.URL.Path != path {
					t.Errorf("path = %s", r.URL.Path)
				}
				if mode == "get" {
					if r.Method != "GET" || r.Header.Get("Idempotency-Key") != "" {
						t.Error("invalid query")
					}
				} else {
					if r.Method != "POST" || r.Header.Get("Idempotency-Key") != "cli-key" {
						t.Error("missing publication key")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if mode == "replay" && len(body) != 0 {
						t.Fatal("recovery selected draft fields")
					}
					if mode == "queue" && (body["includeGatewayLimits"] != true || body["revision"] != "release-1") {
						t.Fatalf("queue body=%v", body)
					}
				}
				w.WriteHeader(202)
				_, _ = io.WriteString(w, `{"operationId":"`+id+`","phase":"queued"}`)
			}))
			defer server.Close()
			args := []string{"--base-url", server.URL, "--deployment", "deployment-a", "--username", "admin", "--password", "fixture-only", "data-plane", "publication", mode}
			if mode != "queue" {
				args = append(args, "--operation-id", id)
			}
			if mode != "get" {
				args = append(args, "--key", "cli-key")
			}
			if mode == "queue" {
				args = append(args, "--revision", "release-1", "--include-gateway-limits")
			}
			command := newRootCommand()
			command.SetArgs(args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("management calls=%d", calls)
			}
		})
	}
}

func TestPublicationFlagsRejectInvalidInputBeforeAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{"queue", "--key", "contains space"},
		{"queue", "--key", strings.Repeat("x", 129)},
		{"replay", "--operation-id", "bad/id", "--key", "key"},
		{"replay", "--operation-id", "81e147fc-c3b9-49b0-8ecf-0a88386734dc", "--key", "newline\n"},
		{"get", "--operation-id", "not-a-uuid"},
	} {
		command := newRootCommand()
		command.SetArgs(append([]string{"--base-url", "http://invalid.invalid", "data-plane", "publication"}, args...))
		if err := command.Execute(); err == nil || strings.Contains(err.Error(), "administrator password") {
			t.Fatalf("flag validation error=%v", err)
		}
	}
}
