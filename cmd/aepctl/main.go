package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type options struct {
	baseURL      string
	deploymentID string
	username     string
	password     string
}

type client struct {
	baseURL string
	token   string
	http    *http.Client
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{Use: "aepctl", Short: "Manage an AEP deployment", SilenceUsage: true}
	root.PersistentFlags().StringVar(&opts.baseURL, "base-url", env("AEPCTL_BASE_URL", "http://localhost:8080"), "AEP service origin")
	root.PersistentFlags().StringVar(&opts.deploymentID, "deployment", env("AEPCTL_DEPLOYMENT", "demo"), "deployment identifier")
	root.PersistentFlags().StringVar(&opts.username, "username", env("AEPCTL_USERNAME", "admin"), "administrator username")
	root.PersistentFlags().StringVar(&opts.password, "password", os.Getenv("AEPCTL_PASSWORD"), "administrator password (prefer AEPCTL_PASSWORD)")
	root.AddCommand(userCommand(opts), skillCommand(opts), credentialCommand(opts), modelCommand(opts), licenseCommand(opts), dataPlaneCommand(opts), eventCommand(opts), sessionCommand(opts), auditCommand(opts), metadataCommand(opts))
	return root
}

func metadataCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "metadata", Short: "Show server capabilities", RunE: func(_ *cobra.Command, _ []string) error {
		api := &client{baseURL: opts.baseURL, http: &http.Client{Timeout: 15 * time.Second}}
		value, err := api.request(http.MethodGet, "/aep/v1/metadata", nil, false)
		return output(value, err)
	}}
}

func userCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "user", Short: "Manage platform users"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/users", nil, true)
		return output(value, err)
	})})
	var username, displayName, password, email string
	var requirePasswordChange bool
	create := &cobra.Command{Use: "create", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		body := map[string]any{"deploymentId": opts.deploymentID, "username": username, "displayName": displayName, "temporaryPassword": password, "requirePasswordChange": requirePasswordChange, "teamIds": []string{}, "roleIds": []string{}}
		if email != "" {
			body["email"] = email
		}
		value, err := api.request(http.MethodPost, "/aep/v1/admin/users", body, true)
		return output(value, err)
	})}
	create.Flags().StringVar(&username, "user", "", "username")
	create.Flags().StringVar(&displayName, "display-name", "", "display name")
	create.Flags().StringVar(&password, "temporary-password", "", "temporary password")
	create.Flags().StringVar(&email, "email", "", "email address")
	create.Flags().BoolVar(&requirePasswordChange, "require-password-change", true, "require a password change at next login")
	_ = create.MarkFlagRequired("user")
	_ = create.MarkFlagRequired("display-name")
	_ = create.MarkFlagRequired("temporary-password")
	var userID string
	disable := &cobra.Command{Use: "disable", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPatch, "/aep/v1/admin/users/"+segment(userID), map[string]any{"status": "disabled"}, true)
		return output(value, err)
	})}
	disable.Flags().StringVar(&userID, "user-id", "", "user identifier")
	_ = disable.MarkFlagRequired("user-id")
	var resetUserID, resetPassword string
	reset := &cobra.Command{Use: "reset-password", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		_, err := api.request(http.MethodPost, "/aep/v1/admin/users/"+segment(resetUserID)+"/reset-password", map[string]any{"temporaryPassword": resetPassword, "requirePasswordChange": true}, true)
		if err == nil {
			fmt.Println("password reset")
		}
		return err
	})}
	reset.Flags().StringVar(&resetUserID, "user-id", "", "user identifier")
	reset.Flags().StringVar(&resetPassword, "temporary-password", "", "temporary password")
	_ = reset.MarkFlagRequired("user-id")
	_ = reset.MarkFlagRequired("temporary-password")
	command.AddCommand(create, disable, reset)
	return command
}

func skillCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "skill", Short: "Manage Skills and assignments"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/skills", nil, true)
		return output(value, err)
	})})
	var skillID, name, description string
	create := &cobra.Command{Use: "create", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPost, "/aep/v1/admin/skills", map[string]any{"id": skillID, "name": name, "description": description, "enabled": true}, true)
		return output(value, err)
	})}
	create.Flags().StringVar(&skillID, "skill-id", "", "Skill identifier")
	create.Flags().StringVar(&name, "name", "", "display name")
	create.Flags().StringVar(&description, "description", "", "description")
	_ = create.MarkFlagRequired("skill-id")
	_ = create.MarkFlagRequired("name")
	var uploadID, version, file string
	upload := &cobra.Command{Use: "upload", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.uploadSkill(uploadID, version, file)
		return output(value, err)
	})}
	upload.Flags().StringVar(&uploadID, "skill-id", "", "Skill identifier")
	upload.Flags().StringVar(&version, "version", "", "version")
	upload.Flags().StringVar(&file, "file", "", "ZIP package path")
	_ = upload.MarkFlagRequired("skill-id")
	_ = upload.MarkFlagRequired("version")
	_ = upload.MarkFlagRequired("file")
	var publishID, publishVersion string
	publish := &cobra.Command{Use: "publish", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPost, "/aep/v1/admin/skills/"+segment(publishID)+"/versions/"+segment(publishVersion)+"/publish", nil, true)
		return output(value, err)
	})}
	publish.Flags().StringVar(&publishID, "skill-id", "", "Skill identifier")
	publish.Flags().StringVar(&publishVersion, "version", "", "version")
	_ = publish.MarkFlagRequired("skill-id")
	_ = publish.MarkFlagRequired("version")
	var assignID, subjectType, subjectID string
	assign := &cobra.Command{Use: "assign", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodPost, "/aep/v1/admin/skill-assignments", map[string]any{"skillId": assignID, "subject": map[string]string{"type": subjectType, "id": subjectID}}, true)
		return output(value, err)
	})}
	assign.Flags().StringVar(&assignID, "skill-id", "", "Skill identifier")
	assign.Flags().StringVar(&subjectType, "subject-type", "user", "user, role, or team")
	assign.Flags().StringVar(&subjectID, "subject-id", "", "subject identifier")
	_ = assign.MarkFlagRequired("skill-id")
	_ = assign.MarkFlagRequired("subject-id")
	var assignmentID string
	revoke := &cobra.Command{Use: "revoke", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		_, err := api.request(http.MethodDelete, "/aep/v1/admin/skill-assignments/"+segment(assignmentID), nil, true)
		if err == nil {
			fmt.Println("assignment removed")
		}
		return err
	})}
	revoke.Flags().StringVar(&assignmentID, "assignment-id", "", "assignment identifier")
	_ = revoke.MarkFlagRequired("assignment-id")
	command.AddCommand(create, upload, publish, assign, revoke)
	return command
}

func eventCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "event", Short: "Publish and inspect control events"}
	var eventType, scopeType, scopeID, skillID, revision string
	var expires time.Duration
	publish := &cobra.Command{Use: "publish", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		scope := map[string]any{"type": scopeType}
		if scopeID != "" {
			scope["id"] = scopeID
		}
		body := map[string]any{"type": eventType, "scope": scope, "task": map[string]string{"type": "skill.reconcile"}, "expiresAt": time.Now().UTC().Add(expires)}
		if skillID != "" {
			body["resource"] = map[string]string{"type": "skill", "id": skillID, "revision": revision}
			body["supersedesKey"] = "skill:" + skillID + ":" + scopeType + ":" + scopeID
		}
		value, err := api.request(http.MethodPost, "/aep/v1/admin/control-events", body, true)
		return output(value, err)
	})}
	publish.Flags().StringVar(&eventType, "type", "skill.manifest.changed", "event type")
	publish.Flags().StringVar(&scopeType, "scope-type", "global", "global, team, or user")
	publish.Flags().StringVar(&scopeID, "scope-id", "", "scope identifier")
	publish.Flags().StringVar(&skillID, "skill-id", "", "optional Skill identifier")
	publish.Flags().StringVar(&revision, "revision", "1", "resource revision")
	publish.Flags().DurationVar(&expires, "expires-in", 24*time.Hour, "event lifetime")
	var eventID string
	deliveries := &cobra.Command{Use: "deliveries", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/control-events/"+segment(eventID)+"/deliveries", nil, true)
		return output(value, err)
	})}
	deliveries.Flags().StringVar(&eventID, "event-id", "", "control event identifier")
	_ = deliveries.MarkFlagRequired("event-id")
	command.AddCommand(publish, deliveries)
	return command
}

func dataPlaneCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "data-plane", Short: "Inspect and publish model gateway desired state"}
	command.AddCommand(&cobra.Command{Use: "status", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/data-plane/status", nil, true)
		return output(value, err)
	})})
	command.AddCommand(&cobra.Command{Use: "desired-state", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/data-plane/desired-state", nil, true)
		return output(value, err)
	})})
	var file string
	publish := &cobra.Command{Use: "publish", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var body map[string]any
		if err := json.Unmarshal(content, &body); err != nil {
			return fmt.Errorf("parse desired state: %w", err)
		}
		value, err := api.request(http.MethodPut, "/aep/v1/admin/data-plane/desired-state", body, true)
		return output(value, err)
	})}
	publish.Flags().StringVar(&file, "file", "", "desired state JSON path")
	_ = publish.MarkFlagRequired("file")
	command.AddCommand(publish)
	return command
}

func sessionCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "session", Short: "Inspect user terminal sessions"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/sessions", nil, true)
		return output(value, err)
	})})
	return command
}

func auditCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "audit", Short: "Query telemetry", RunE: authenticated(opts, func(api *client, _ *cobra.Command, _ []string) error {
		value, err := api.request(http.MethodGet, "/aep/v1/admin/events", nil, true)
		return output(value, err)
	})}
	return command
}

func authenticated(opts *options, run func(*client, *cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(command *cobra.Command, args []string) error {
		if opts.password == "" {
			return errors.New("administrator password is required through --password or AEPCTL_PASSWORD")
		}
		api := &client{baseURL: strings.TrimRight(opts.baseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}}
		if err := api.login(opts); err != nil {
			return err
		}
		return run(api, command, args)
	}
}
func (c *client) login(opts *options) error {
	value, err := c.request(http.MethodPost, "/aep/v1/auth/password/login", map[string]any{"deploymentId": opts.deploymentID, "username": opts.username, "password": opts.password}, false)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return errors.New("invalid login response")
	}
	token, ok := object["accessToken"].(string)
	if !ok {
		return errors.New("login response did not include accessToken")
	}
	c.token = token
	return nil
}
func (c *client) request(method, path string, body any, authenticated bool) (any, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-AEP-Protocol-Version", "1.0")
	request.Header.Set("X-Request-ID", uuid.NewString())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("AEP request failed (%d): %s", response.StatusCode, strings.TrimSpace(string(content)))
	}
	if len(content) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(content, &value); err != nil {
		return nil, err
	}
	return value, nil
}
func (c *client) uploadSkill(skillID, version, path string) (any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("version", version)
	part, err := writer.CreateFormFile("package", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(content); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, c.baseURL+"/aep/v1/admin/skills/"+segment(skillID)+"/versions", &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-AEP-Protocol-Version", "1.0")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	contentResponse, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("upload failed (%d): %s", response.StatusCode, contentResponse)
	}
	var value any
	if err := json.Unmarshal(contentResponse, &value); err != nil {
		return nil, err
	}
	return value, nil
}
func output(value any, err error) error {
	if err != nil {
		return err
	}
	if value == nil {
		return nil
	}
	encoded, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(encoded))
	return nil
}
func segment(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%", "%25"), "/", "%2F")
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func platform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}
