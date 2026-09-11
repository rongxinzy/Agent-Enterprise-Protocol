package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSkillResourceWriteLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "skills"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created, err := store.CreateSkill(context.Background(), Skill{ID: "writer", Name: "Writer", Description: "Drafts", Enabled: true})
	if err != nil || created.ID != "writer" {
		t.Fatalf("CreateSkill() = %#v, %v", created, err)
	}

	name := "Writer 2"
	enabled := false
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "skills" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).WithArgs("writer", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "enabled", "created_at", "updated_at"}).AddRow("writer", name, "Updated", enabled, now, now))
	updated, err := store.UpdateSkill(context.Background(), "writer", UpdateSkillParams{Name: &name, Enabled: &enabled})
	if err != nil || updated.Name != name || updated.Enabled {
		t.Fatalf("UpdateSkill() = %#v, %v", updated, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "skills" WHERE id = \$1`).WithArgs("writer").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.DeleteSkill(context.Background(), "writer"); err != nil {
		t.Fatalf("DeleteSkill() = %v", err)
	}
}

func TestCredentialResourceWriteLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	credential := Credential{ID: "provider", Name: "Provider", Service: "deepseek", Type: "api_key", DeliveryMode: "server_only", EncryptedValue: []byte("cipher"), Nonce: []byte("nonce"), KeyID: "key-a", MaskedValue: "****cret", Enabled: true}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "credentials"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created, err := store.Deployment("deployment-a").CreateCredential(context.Background(), credential)
	if err != nil || created.DeploymentID != "deployment-a" || created.ID != "provider" {
		t.Fatalf("CreateCredential() = %#v, %v", created, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "provider", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "service", "type", "delivery_mode", "encrypted_value", "nonce", "key_id", "masked_value", "enabled", "created_at", "updated_at", "rotated_at"}).
			AddRow("deployment-a", "provider", "Provider", "deepseek", "api_key", "server_only", []byte("cipher"), []byte("nonce"), "key-a", "****cret", true, now, now, now))
	got, err := store.Deployment("deployment-a").GetCredential(context.Background(), "provider")
	if err != nil || got.ID != "provider" || string(got.EncryptedValue) != "cipher" {
		t.Fatalf("GetCredential() = %#v, %v", got, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "credentials" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "provider", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "service", "type", "delivery_mode", "encrypted_value", "nonce", "key_id", "masked_value", "enabled", "created_at", "updated_at", "rotated_at"}).
			AddRow("deployment-a", "provider", "Provider", "deepseek", "api_key", "server_only", []byte("rotated"), []byte("nonce-2"), "key-b", "****ated", true, now, now, now))
	rotated, err := store.Deployment("deployment-a").RotateCredential(context.Background(), "provider", []byte("rotated"), []byte("nonce-2"), "key-b", "****ated")
	if err != nil || string(rotated.EncryptedValue) != "rotated" || rotated.KeyID != "key-b" {
		t.Fatalf("RotateCredential() = %#v, %v", rotated, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).WithArgs("deployment-a", "provider").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteCredential(context.Background(), "provider"); err != nil {
		t.Fatalf("DeleteCredential() = %v", err)
	}
}

func TestModelReadLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	columns := []string{"deployment_id", "id", "display_name", "source_type", "protocol", "endpoint", "upstream_model", "local_model_ref", "credential_id", "capabilities", "reasoning_compatibility", "context_window", "is_default", "enabled", "created_at", "updated_at"}
	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 ORDER BY id LIMIT \$2`).
		WithArgs("deployment-a", 2).WillReturnRows(sqlmock.NewRows(columns).
		AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://gateway", "deepseek-chat", nil, nil, `{text}`, []byte(`{"mode":"native"}`), 8192, true, true, now, now))
	models, err := store.Deployment("deployment-a").ListModelsPage(context.Background(), "", 2)
	if err != nil || len(models) != 1 || models[0].ID != "chat-a" {
		t.Fatalf("ListModelsPage() = %#v, %v", models, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "chat-a", 1).WillReturnRows(sqlmock.NewRows(columns).
		AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", "http://gateway", "deepseek-chat", nil, nil, `{text}`, []byte(`{"mode":"native"}`), 8192, true, true, now, now))
	model, err := store.Deployment("deployment-a").GetModel(context.Background(), "chat-a")
	if err != nil || model.ID != "chat-a" || model.DisplayName != "Chat A" {
		t.Fatalf("GetModel() = %#v, %v", model, err)
	}
}
