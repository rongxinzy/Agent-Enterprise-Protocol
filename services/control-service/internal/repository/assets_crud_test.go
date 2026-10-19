package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func assetColumns() []string {
	return []string{
		"deployment_id", "id", "type_id", "name", "description", "org_team_id", "location_id", "owner_user_id",
		"undertakers", "spec", "credential_refs", "capabilities_override", "status",
		"health_state", "health_checked_at", "health_detail", "source", "created_at", "updated_at",
	}
}

func assetRow(id, name string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(assetColumns()).
		AddRow("deployment-a", id, "ssh_server", name, nil, nil, nil, nil,
			`[{"type":"de_employee","id":"net-admin"}]`,
			`{"host":"10.0.0.5","port":22,"username":"ops","authMethod":"private_key"}`,
			`{"default":"credential-a"}`, `{}`, "active",
			nil, nil, nil, "manual", now, now)
}

func TestAssetCreateGetUpdateDelete(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "assets"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created, err := store.Deployment("deployment-a").CreateAsset(context.Background(), Asset{
		ID: "asset-a", TypeID: "ssh_server", Name: "Bastion",
		Spec: JSONObject{"host": "10.0.0.5", "username": "ops", "authMethod": "private_key"},
		CredentialRefs: JSONObject{"default": "credential-a"},
		Status:         "active", Source: "manual",
	})
	if err != nil || created.DeploymentID != "deployment-a" || created.ID != "asset-a" {
		t.Fatalf("CreateAsset() = %#v, %v", created, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetRow("asset-a", "Bastion"))
	record, err := store.Deployment("deployment-a").GetAsset(context.Background(), "asset-a")
	if err != nil || record.ID != "asset-a" || record.TypeID != "ssh_server" {
		t.Fatalf("GetAsset() = %#v, %v", record, err)
	}
	if record.Spec["host"] != "10.0.0.5" || record.CredentialRefs["default"] != "credential-a" {
		t.Fatalf("GetAsset() JSON columns did not round-trip: %#v", record)
	}
	if len(record.Undertakers) != 1 {
		t.Fatalf("GetAsset() undertakers did not round-trip: %#v", record.Undertakers)
	}

	newName := "Bastion Two"
	status := "disabled"
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "assets" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-a", 1).WillReturnRows(assetRow("asset-a", "Bastion Two"))
	updated, err := store.Deployment("deployment-a").UpdateAsset(context.Background(), "asset-a", UpdateAssetParams{
		Name: &newName, Status: &status,
	})
	if err != nil || updated.Name != "Bastion Two" {
		t.Fatalf("UpdateAsset() = %#v, %v", updated, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "assets" SET`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	_, err = store.Deployment("deployment-a").UpdateAsset(context.Background(), "missing", UpdateAssetParams{Name: &newName})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateAsset(missing) = %v, want ErrNotFound", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "assets" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "asset-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteAsset(context.Background(), "asset-a"); err != nil {
		t.Fatalf("DeleteAsset() = %v", err)
	}
}

func TestAssetJSONColumnRoundTrip(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(`SELECT \* FROM "assets" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "asset-b", 1).
		WillReturnRows(sqlmock.NewRows(assetColumns()).
			AddRow("deployment-a", "asset-b", "ssh_server", "Empty", nil, nil, nil, nil,
				nil, `{}`, nil, nil, "draft", nil, nil, nil, "manual", time.Now(), time.Now()))
	record, err := store.Deployment("deployment-a").GetAsset(context.Background(), "asset-b")
	if err != nil {
		t.Fatalf("GetAsset() = %v", err)
	}
	if len(record.Spec) != 0 || len(record.Undertakers) != 0 || len(record.CapabilitiesOverride) != 0 {
		t.Fatalf("nil JSON columns should scan to empty, got %#v", record)
	}

	value, err := JSONObject(nil).Value()
	if err != nil || value != "{}" {
		t.Fatalf("JSONObject(nil).Value() = %v, %v", value, err)
	}
	array, err := JSONArray(nil).Value()
	if err != nil || array != "[]" {
		t.Fatalf("JSONArray(nil).Value() = %v, %v", array, err)
	}
	var scanned JSONObject
	if err := scanned.Scan([]byte(`{"default":"credential-a"}`)); err != nil || scanned["default"] != "credential-a" {
		t.Fatalf("Scan([]byte) = %#v, %v", scanned, err)
	}
	var scannedArray JSONArray
	if err := scannedArray.Scan(`["exec"]`); err != nil || len(scannedArray) != 1 {
		t.Fatalf("Scan(string) = %#v, %v", scannedArray, err)
	}
	if err := scanned.Scan(42); err == nil {
		t.Fatalf("Scan(int) should fail")
	}
}

func TestAssetBindingLifecycle(t *testing.T) {
	store, mock := newMockStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "asset_bindings"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created, err := store.Deployment("deployment-a").CreateAssetBinding(context.Background(), AssetBinding{
		ID: "binding-a", AssetID: "asset-a", SubjectType: "companion", SubjectID: "demo",
		CapabilityAllowlist: JSONArray{"exec"}, GrantedBy: "admin-user",
	})
	if err != nil || created.ID != "binding-a" || created.DeploymentID != "deployment-a" {
		t.Fatalf("CreateAssetBinding() = %#v, %v", created, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "asset_bindings" WHERE deployment_id = \$1 AND asset_id = \$2`).
		WithArgs("deployment-a", "asset-a", 51).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "asset_id", "subject_type", "subject_id", "capability_allowlist", "granted_by", "expires_at", "created_at"}).
			AddRow("binding-a", "deployment-a", "asset-a", "companion", "demo", `["exec"]`, "admin-user", nil, time.Now()))
	bindings, err := store.Deployment("deployment-a").ListAssetBindingsPage(context.Background(), "asset-a", "", 51)
	if err != nil || len(bindings) != 1 || bindings[0].SubjectID != "demo" {
		t.Fatalf("ListAssetBindingsPage() = %#v, %v", bindings, err)
	}
	if len(bindings[0].CapabilityAllowlist) != 1 || bindings[0].CapabilityAllowlist[0] != "exec" {
		t.Fatalf("capability allowlist did not round-trip: %#v", bindings[0].CapabilityAllowlist)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "asset_bindings" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "binding-a").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteAssetBinding(context.Background(), "binding-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteAssetBinding(missing) = %v, want ErrNotFound", err)
	}
}
