package httpapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
)

func TestGatewayTestAccessSingleModel(t *testing.T) {
	for _, protocol := range []string{"openai-compatible", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			a, mock, token := newStoreBackedHTTPApplication(t)
			pool := attachRuntimeDatabase(t, a)
			now := time.Now().UTC()
			a.Config.ModelGatewayBaseURL = "http://127.0.0.1:8090/v1"
			mock.ExpectQuery(`SELECT \* FROM "models"`).WithArgs("deployment-a", "model-a", 1).WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "source_type", "protocol", "enabled"}).AddRow("deployment-a", "model-a", "gateway", protocol, true))
			pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "admin-user").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("model-a").AddRow("other-model"))
			pool.ExpectQuery(`SELECT model_gateway_base_url`).WithArgs("deployment-a").WillReturnError(pgx.ErrNoRows)
			got := adminRequest(New(a).Handler(), token, http.MethodPost, "/aep/v1/admin/model-gateway/models/model-a/test-access", "")
			if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(got.Code, got.Body.String())
			}
			var access struct {
				ModelAccessToken string    `json:"modelAccessToken"`
				BaseURL          string    `json:"baseUrl"`
				Path             string    `json:"path"`
				ExpiresAt        time.Time `json:"expiresAt"`
			}
			if err := json.Unmarshal(got.Body.Bytes(), &access); err != nil {
				t.Fatal(err)
			}
			claims, err := a.Tokens.ParseModel(access.ModelAccessToken)
			if err != nil || claims.Admin || claims.TokenUse != "model" || claims.SessionID != "session-admin" || claims.DeploymentID != "deployment-a" || len(claims.ModelScopes) != 1 || claims.ModelScopes[0] != "model-a" || !claims.ExpiresAt.Equal(access.ExpiresAt) || access.ExpiresAt.After(now.Add(2*time.Minute)) {
				t.Fatal("test scope/lifetime")
			}
			if protocol == "anthropic" && (access.BaseURL != "http://127.0.0.1:8090/model-a" || access.Path != "/v1/messages") {
				t.Fatal(access.BaseURL, access.Path)
			}
			if protocol == "openai-compatible" && (access.BaseURL != "http://127.0.0.1:8090/v1" || access.Path != "/chat/completions") {
				t.Fatal(access.BaseURL, access.Path)
			}
		})
	}
}

func TestGatewayTestAccessAuthorizationFailures(t *testing.T) {
	for _, failure := range []string{"not-found", "disabled", "local", "scope", "scopes-outage", "settings-outage", "gateway-missing", "production-license"} {
		t.Run(failure, func(t *testing.T) {
			a, mock, token := newStoreBackedHTTPApplication(t)
			pool := attachRuntimeDatabase(t, a)
			a.Config.ModelGatewayBaseURL = "http://127.0.0.1:8090/v1"
			model := mock.ExpectQuery(`SELECT \* FROM "models"`).WithArgs("deployment-a", "model-a", 1)
			if failure == "not-found" {
				model.WillReturnRows(sqlmock.NewRows([]string{"id"}))
			} else {
				source := "gateway"
				if failure == "local" {
					source = "local"
				}
				model.WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "source_type", "protocol", "enabled"}).AddRow("deployment-a", "model-a", source, "openai-compatible", failure != "disabled"))
			}
			if failure != "not-found" && failure != "disabled" && failure != "local" {
				q := pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "admin-user")
				switch failure {
				case "scopes-outage":
					q.WillReturnError(errors.New("scope unavailable"))
				case "scope":
					q.WillReturnRows(pgxmock.NewRows([]string{"id"}))
				default:
					q.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("model-a"))
				}
				if failure != "scope" && failure != "scopes-outage" {
					q := pool.ExpectQuery(`SELECT model_gateway_base_url`).WithArgs("deployment-a")
					if failure == "settings-outage" {
						q.WillReturnError(errors.New("settings unavailable"))
					} else {
						q.WillReturnError(pgx.ErrNoRows)
					}
				}
			}
			if failure == "gateway-missing" {
				a.Config.ModelGatewayBaseURL = ""
			}
			if failure == "production-license" {
				a.Config.Environment = "production"
			}
			got := adminRequest(New(a).Handler(), token, "POST", "/aep/v1/admin/model-gateway/models/model-a/test-access", "")
			if got.Code < 400 || strings.Contains(got.Body.String(), "modelAccessToken") {
				t.Fatal(failure, got.Code, got.Body.String())
			}
		})
	}
}

func TestGatewayTestAccessLicensedSession(t *testing.T) {
	for _, state := range []string{"active", "perpetual", "grace", "expired", "revoked", "foreign", "missing-feature", "activation-outage"} {
		t.Run(state, func(t *testing.T) {
			a, mock, token := newStoreBackedHTTPApplication(t)
			pool := attachRuntimeDatabase(t, a)
			a.Config.Environment = "production"
			a.Config.ModelGatewayBaseURL = "https://default.example/v1"
			now := time.Now().UTC()
			expiry := now.Add(45 * time.Second)
			graceDays := 0
			dep := "deployment-a"
			features := []string{"enterprise.models"}
			switch state {
			case "grace":
				expiry = now.Add(-time.Hour)
				graceDays = 1
			case "expired":
				expiry = now.Add(-time.Hour)
			case "foreign":
				dep = "deployment-b"
			case "missing-feature":
				features = []string{}
			}
			public, private, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			var expires any = expiry.Format("2006-01-02T15:04:05.000Z")
			if state == "perpetual" {
				expires = nil
			}
			payload, err := json.Marshal(map[string]any{"customerId": "test-customer", "deploymentId": dep, "edition": "enterprise", "expiresAt": expires, "features": features, "graceDays": graceDays, "issuedAt": now.Add(-24 * time.Hour).Format("2006-01-02T15:04:05.000Z"), "licenseId": "test-license"})
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := json.Marshal(license.Envelope{Format: "zhiyuan-license-v1", KeyID: "test-key", Payload: payload, Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, payload))})
			if err != nil {
				t.Fatal(err)
			}
			verifier, err := license.NewVerifier(map[string]string{"test-key": base64.RawURLEncoding.EncodeToString(public)}, dep)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := verifier.Verify(envelope)
			if err != nil {
				t.Fatal(err)
			}
			a.LicenseVerifier = verifier
			a.SetLicense(verified)
			mock.ExpectQuery(`SELECT \* FROM "models"`).WithArgs("deployment-a", "model-a", 1).WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "source_type", "protocol", "enabled"}).AddRow("deployment-a", "model-a", "gateway", "openai-compatible", true))
			pool.ExpectQuery(`SELECT DISTINCT m.id`).WithArgs("deployment-a", "admin-user").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("model-a"))
			pool.ExpectQuery(`SELECT model_gateway_base_url`).WithArgs("deployment-a").WillReturnRows(pgxmock.NewRows([]string{"model_gateway_base_url"}).AddRow("https://tenant.example/v1"))
			want := 200
			if state == "expired" || state == "foreign" || state == "missing-feature" {
				want = 403
			} else {
				pool.ExpectBegin()
				if state == "activation-outage" {
					pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).WithArgs("test-license", "deployment-a").WillReturnError(errors.New("unavailable"))
				} else {
					status := "active"
					if state == "revoked" {
						status = "revoked"
					}
					pool.ExpectQuery(`SELECT status, revoked_at FROM licenses`).WithArgs("test-license", "deployment-a").WillReturnRows(pgxmock.NewRows([]string{"status", "revoked_at"}).AddRow(status, nil))
				}
				if state == "revoked" || state == "activation-outage" {
					want = 403
					pool.ExpectRollback()
				} else {
					pool.ExpectQuery(`SELECT EXISTS`).WithArgs("test-license", "deployment-a").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
					pool.ExpectExec(`UPDATE license_activations`).WithArgs("test-license", "deployment-a").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					pool.ExpectCommit()
				}
			}
			got := adminRequest(New(a).Handler(), token, "POST", "/aep/v1/admin/model-gateway/models/model-a/test-access", "")
			if got.Code != want {
				t.Fatal(got.Code, got.Body.String())
			}
			if want != 200 {
				if strings.Contains(got.Body.String(), "modelAccessToken") {
					t.Fatal("inactive license received a test token")
				}
				return
			}
			var access struct {
				ModelAccessToken string    `json:"modelAccessToken"`
				BaseURL          string    `json:"baseUrl"`
				ExpiresAt        time.Time `json:"expiresAt"`
			}
			if err := json.Unmarshal(got.Body.Bytes(), &access); err != nil {
				t.Fatal(err)
			}
			claims, err := a.Tokens.ParseEntitlement(access.ModelAccessToken)
			if err != nil || claims.SessionID != "session-admin" || claims.LicenseID != "test-license" || claims.Admin || len(claims.ModelScopes) != 1 || claims.ModelScopes[0] != "model-a" || !claims.ExpiresAt.Equal(access.ExpiresAt) || access.ExpiresAt.After(now.Add(2*time.Minute)) || access.BaseURL != "https://tenant.example/v1" {
				t.Fatal("licensed test binding/lifetime/endpoint")
			}
			if state == "active" && access.ExpiresAt.After(expiry) {
				t.Fatal("test access exceeded license expiry")
			}
		})
	}
}
