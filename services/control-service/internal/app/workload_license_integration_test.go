package app

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	database "github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/db"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/license"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayaccess"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"k8s.io/apimachinery/pkg/types"
)

// No static signer or customer material: each execution signs disposable claims
// in memory and uses the production verifier and RegisterLicense entry boundary.
func verifiedWorkloadTestLicense(t *testing.T, id, feature string) license.Verified {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"licenseId": id, "customerId": "fixture-customer", "deploymentId": "licensed-business", "edition": "enterprise",
		"issuedAt":  time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z"),
		"expiresAt": nil, "graceDays": 0, "features": []string{feature},
	}
	canonical, err := json.Marshal(payload) // ASCII fixture; sorted map keys match the license canonical form.
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"format": "zhiyuan-license-v1", "keyId": "fixture-key", "payload": payload,
		"signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, canonical)),
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier := license.Verifier{TrustedKeys: map[string]ed25519.PublicKey{"fixture-key": public}, ExpectedDeployment: "licensed-business"}
	verified, err := verifier.Verify(raw)
	if err != nil {
		t.Fatal("synthetic license signature failed verification", err)
	}
	return verified
}

func workloadLicenseTestApp(t *testing.T) *App {
	t.Helper()
	url := os.Getenv("AEP_REFERENCE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AEP_REFERENCE_TEST_DATABASE_URL to a disposable PostgreSQL server")
	}
	admin, err := pgxpool.New(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "workload_license_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP DATABASE `+name); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	orm, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return &App{Config: config.Config{LicenseDeploymentID: "licensed-business"}, Pool: pool, Store: repository.New(orm)}
}

func TestVerifiedLicenseRegistrationFeedsWorkloadPublicationWithPostgres(t *testing.T) {
	a := workloadLicenseTestApp(t)
	if _, err := a.Pool.Exec(t.Context(), `
INSERT INTO deployments(id,name) VALUES('licensed-business','Fixture');
INSERT INTO roles(deployment_id,id,name) VALUES('licensed-business','model-role','Fixture');
INSERT INTO teams(deployment_id,id,name) VALUES('licensed-business','employee-team','Fixture');
INSERT INTO users(id,deployment_id,username,display_name,password_hash,require_password_change,kind)
  VALUES('employee','licensed-business','employee','Fixture','test-only-unused',false,'agent');
INSERT INTO agent_profiles(deployment_id,user_id,home_team_id) VALUES('licensed-business','employee','employee-team');
INSERT INTO user_role_bindings(deployment_id,user_id,role_id) VALUES('licensed-business','employee','model-role');
INSERT INTO models(deployment_id,id,display_name,source_type,protocol,enabled,endpoint)
  VALUES('licensed-business','chat','Fixture','gateway','openai-compatible',true,'http://fixture.invalid');
INSERT INTO model_assignments(id,deployment_id,model_id,subject_type,subject_id)
  VALUES('role-grant','licensed-business','chat','role','model-role');`); err != nil {
		t.Fatal(err)
	}
	store := a.Store.Deployment("licensed-business")
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", repository.ObservedModelWorkload{
		EmployeeName: "employee", Identity: gateway.ModelAccessWorkload{Namespace: "employees", EmployeeUID: types.UID("employee-uid"),
			ServiceAccountName: "employee-model", ServiceAccountUID: types.UID("sa-uid"), Audience: "fixture-audience"},
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"enterprise.skills", "enterprise.models"} {
		verified := verifiedWorkloadTestLicense(t, feature+"-license", feature)
		if err := a.RegisterLicense(t.Context(), verified); err != nil {
			t.Fatal("verified registration failed", err)
		}
		capture, err := store.CaptureModelAccessBindings(t.Context(), "employee", time.Minute)
		if err != nil || capture == nil || len(capture.Publications) != 1 {
			t.Fatal("business publication missing after verified registration", err)
		}
		publication := capture.Publications[0]
		policy, err := gatewayaccess.Decode(publication.Payload, publication.ContentHash)
		if err != nil {
			t.Fatal(err)
		}
		if feature != "enterprise.models" {
			if !policy.Disabled || policy.WorkloadEntitlement != nil || policy.Retired {
				t.Fatal("valid signature for an unrelated feature was treated as model entitlement")
			}
			continue
		}
		if policy.Disabled || policy.WorkloadEntitlement == nil || policy.WorkloadEntitlement.LicenseID != verified.Claims.LicenseID ||
			policy.WorkloadEntitlement.LicenseDigest != verified.Digest || policy.Subject.ID != "employee" || len(policy.Models) != 1 || len(policy.Licenses) != 1 {
			t.Fatal("signed registration lost exact license evidence or role-derived employee model access")
		}
	}
}
