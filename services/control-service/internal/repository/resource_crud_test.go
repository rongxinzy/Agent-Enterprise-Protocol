package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"
)

func TestStringArrayDatabaseContracts(t *testing.T) {
	var values StringArray
	if err := values.Scan(nil); err != nil || values != nil {
		t.Fatalf("Scan(nil) = %#v, %v", values, err)
	}
	if err := values.Scan([]string{"text", "reasoning"}); err != nil || len(values) != 2 {
		t.Fatalf("Scan([]string) = %#v, %v", values, err)
	}
	if err := values.Scan(`{streaming,tools}`); err != nil || strings.Join(values, ",") != "streaming,tools" {
		t.Fatalf("Scan(text array) = %#v, %v", values, err)
	}
	if err := values.Scan([]byte(`{vision}`)); err != nil || len(values) != 1 || values[0] != "vision" {
		t.Fatalf("Scan(binary value) = %#v, %v", values, err)
	}
	if err := values.Scan(42); err == nil {
		t.Fatal("Scan accepted an unsupported source type")
	}
	encoded, err := (StringArray{"text", "reasoning"}).Value()
	if err != nil || !strings.Contains(encoded.(string), "text") || !strings.Contains(encoded.(string), "reasoning") {
		t.Fatalf("Value() = %#v, %v", encoded, err)
	}
}

func TestSkillQueriesAndVersionLifecycle(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id > \$1 ORDER BY id LIMIT \$2`).
		WithArgs("skill-0", 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "enabled", "created_at", "updated_at"}).
			AddRow("skill-a", "Skill A", "Description", true, now, now))
	skills, err := store.ListSkillsPage(context.Background(), "skill-0", 2)
	if err != nil || len(skills) != 1 || skills[0].ID != "skill-a" {
		t.Fatalf("ListSkillsPage() = %#v, %v", skills, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 ORDER BY created_at DESC, version DESC`).
		WithArgs("skill-a").
		WillReturnRows(sqlmock.NewRows([]string{"skill_id", "version", "object_key", "sha256", "size_bytes", "published", "created_at", "published_at"}).
			AddRow("skill-a", "1.0.0", "skills/a.zip", "sha256:a", 128, true, now, now))
	versions, err := store.ListSkillVersions(context.Background(), "skill-a")
	if err != nil || len(versions) != 1 || versions[0].ObjectKey != "skills/a.zip" {
		t.Fatalf("ListSkillVersions() = %#v, %v", versions, err)
	}

	mock.ExpectExec(`INSERT INTO skill_versions`).
		WithArgs("skill-a", "1.1.0", "skills/a-1.1.zip", "sha256:b", int64(256)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.UpsertSkillVersion(context.Background(), SkillVersion{
		SkillID: "skill-a", Version: "1.1.0", ObjectKey: "skills/a-1.1.zip", SHA256: "sha256:b", SizeBytes: 256,
	}); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "skill_versions" SET "published"=\$1,"published_at"=\$2 WHERE skill_id = \$3 AND version = \$4`).
		WithArgs(true, sqlmock.AnyArg(), "skill-a", "1.1.0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.PublishSkillVersion(context.Background(), "skill-a", "1.1.0"); err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery(`SELECT \* FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2 LIMIT \$3`).
		WithArgs("skill-a", "1.0.0", 1).
		WillReturnRows(sqlmock.NewRows([]string{"skill_id", "version", "object_key", "sha256", "size_bytes", "published", "created_at"}).
			AddRow("skill-a", "1.0.0", "skills/a.zip", "sha256:a", 128, false, now))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "skill_versions" WHERE skill_id = \$1 AND version = \$2`).
		WithArgs("skill-a", "1.0.0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	objectKey, err := store.DeleteSkillVersion(context.Background(), "skill-a", "1.0.0")
	if err != nil || objectKey != "skills/a.zip" {
		t.Fatalf("DeleteSkillVersion() = %q, %v", objectKey, err)
	}
}

func TestCredentialQueriesEnforceDeploymentScope(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE deployment_id = \$1 AND id > \$2 ORDER BY id LIMIT \$3`).
		WithArgs("deployment-a", "credential-0", 2).
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_id", "id", "name", "service", "type", "delivery_mode", "encrypted_value", "nonce", "key_id", "masked_value", "enabled", "created_at", "updated_at", "rotated_at",
		}).AddRow("deployment-a", "credential-a", "Provider", "deepseek", "api_key", "server_only", []byte("cipher"), []byte("nonce"), "key-a", "sk-***", true, now, now, now))
	credentials, err := store.Deployment("deployment-a").ListCredentialsPage(context.Background(), "credential-0", 2)
	if err != nil || len(credentials) != 1 || credentials[0].DeploymentID != "deployment-a" {
		t.Fatalf("ListCredentialsPage() = %#v, %v", credentials, err)
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "credentials" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "credential-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	exists, err := store.Deployment("deployment-a").HasCredential(context.Background(), "credential-a")
	if err != nil || !exists {
		t.Fatalf("HasCredential() = %v, %v", exists, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "credential_assignments" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "assignment-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteCredentialAssignment(context.Background(), "assignment-a"); err != nil {
		t.Fatal(err)
	}
}

func TestModelQueriesAndAssignmentsEnforceDeploymentScope(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	columns := []string{
		"deployment_id", "id", "display_name", "source_type", "protocol", "endpoint", "upstream_model", "local_model_ref", "credential_id",
		"capabilities", "reasoning_compatibility", "context_window", "is_default", "enabled", "created_at", "updated_at",
	}
	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 AND enabled = \$2 AND id IN \(\$3,\$4\) ORDER BY is_default DESC, id`).
		WithArgs("deployment-a", true, "chat-a", "chat-b").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("deployment-a", "chat-a", "Chat A", "gateway", "openai-compatible", nil, nil, nil, nil, `{text}`, nil, nil, true, true, now, now))
	models, err := store.Deployment("deployment-a").ListEnabledModelsByIDs(context.Background(), []string{"chat-a", "chat-b"})
	if err != nil || len(models) != 1 || models[0].ID != "chat-a" || len(models[0].Capabilities) != 1 {
		t.Fatalf("ListEnabledModelsByIDs() = %#v, %v", models, err)
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "chat-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	exists, err := store.Deployment("deployment-a").HasModel(context.Background(), "chat-a")
	if err != nil || !exists {
		t.Fatalf("HasModel() = %v, %v", exists, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "model_assignments"`).
		WithArgs("assignment-a", "deployment-a", "chat-a", "team", "team-a", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	assignment, err := store.Deployment("deployment-a").CreateModelAssignment(context.Background(), ModelAssignment{
		ID: "assignment-a", ModelID: "chat-a", SubjectType: "team", SubjectID: "team-a",
	})
	if err != nil || assignment.DeploymentID != "deployment-a" {
		t.Fatalf("CreateModelAssignment() = %#v, %v", assignment, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "model_assignments" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "assignment-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteModelAssignment(context.Background(), "assignment-a"); err != nil {
		t.Fatal(err)
	}
}

func TestRoleAndTeamAggregateQueries(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "roles" WHERE deployment_id = \$1 AND id > \$2 ORDER BY id LIMIT \$3`).
		WithArgs("deployment-a", "role-0", 3).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "role-a", "Operator", "Operates models", false, true, now, now).
			AddRow("deployment-a", "role-b", "Auditor", "Reads audit", false, true, now, now))
	mock.ExpectQuery(`SELECT \* FROM "role_permissions" WHERE deployment_id = \$1 AND role_id IN \(\$2,\$3\) ORDER BY role_id, permission_id`).
		WithArgs("deployment-a", "role-a", "role-b").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "role_id", "permission_id"}).
			AddRow("deployment-a", "role-a", "models.read"))
	roles, err := store.Deployment("deployment-a").ListRolesPage(context.Background(), "role-0", 3)
	if err != nil || len(roles) != 2 || len(roles[0].Permissions) != 1 || roles[1].Permissions == nil {
		t.Fatalf("ListRolesPage() = %#v, %v", roles, err)
	}

	mock.ExpectQuery(`SELECT teams\.\*, COUNT\(user_team_bindings\.user_id\) AS member_count FROM "teams" LEFT JOIN user_team_bindings`).
		WithArgs("deployment-a", "team-0", 2).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at", "member_count"}).
			AddRow("deployment-a", "team-a", "Engineering", "Builders", false, true, now, now, 4))
	teams, err := store.Deployment("deployment-a").ListTeamsPage(context.Background(), "team-0", 2)
	if err != nil || len(teams) != 1 || teams[0].MemberCount != 4 {
		t.Fatalf("ListTeamsPage() = %#v, %v", teams, err)
	}

	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM role_permissions AS rp JOIN user_role_bindings AS urb`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("models.read").AddRow("skills.read"))
	permissions, err := store.Deployment("deployment-a").UserPermissionIDs(context.Background(), "user-a")
	if err != nil || strings.Join(permissions, ",") != "models.read,skills.read" {
		t.Fatalf("UserPermissionIDs() = %#v, %v", permissions, err)
	}
}

func TestRepositoryNotFoundAndDatabaseErrors(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "credentials"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	name := "Renamed"
	if _, err := store.Deployment("deployment-a").UpdateCredential(context.Background(), "missing", UpdateCredentialParams{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateCredential() error = %v", err)
	}

	mock.ExpectQuery(`SELECT \* FROM "skills" WHERE id = \$1 LIMIT \$2`).
		WithArgs("missing", 1).WillReturnError(gorm.ErrRecordNotFound)
	if _, err := store.GetSkill(context.Background(), "missing"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("GetSkill() error = %v", err)
	}
}

func TestDeploymentAndIdentityLookupContracts(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectExec(`INSERT INTO deployments`).
		WithArgs("deployment-a", "Deployment A").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT \* FROM "deployments" WHERE id = \$1 LIMIT \$2`).
		WithArgs("deployment-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "created_at"}).AddRow("deployment-a", "Deployment A", now))
	deployment, err := store.UpsertDeployment(context.Background(), Deployment{ID: "deployment-a", Name: "Deployment A"})
	if err != nil || deployment.Name != "Deployment A" {
		t.Fatalf("UpsertDeployment() = %#v, %v", deployment, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND username = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "username", "display_name", "status", "password_hash", "require_password_change", "is_admin", "created_at", "updated_at"}).
			AddRow("user-a", "deployment-a", "alice", "Alice", "active", "hash", false, false, now, now))
	user, err := store.Deployment("deployment-a").GetUserByUsername(context.Background(), "alice")
	if err != nil || user.ID != "user-a" {
		t.Fatalf("GetUserByUsername() = %#v, %v", user, err)
	}

	mock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings" WHERE deployment_id = \$1 AND user_id = \$2 ORDER BY role_id`).
		WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("member"))
	roles, err := store.Deployment("deployment-a").UserRoleIDs(context.Background(), "user-a")
	if err != nil || len(roles) != 1 || roles[0] != "member" {
		t.Fatalf("UserRoleIDs() = %#v, %v", roles, err)
	}

	mock.ExpectQuery(`SELECT "team_id" FROM "user_team_bindings" WHERE deployment_id = \$1 AND user_id = \$2 ORDER BY team_id`).
		WithArgs("deployment-a", "user-a").WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("team-a"))
	teams, err := store.Deployment("deployment-a").UserTeamIDs(context.Background(), "user-a")
	if err != nil || len(teams) != 1 || teams[0] != "team-a" {
		t.Fatalf("UserTeamIDs() = %#v, %v", teams, err)
	}
}

func TestAssignmentListsAndCredentialCreation(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "skill_assignments" WHERE deployment_id = \$1 ORDER BY id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "skill_id", "subject_type", "subject_id", "created_at"}).
			AddRow("assignment-s", "deployment-a", "skill-a", "team", "team-a", now))
	skills, err := store.Deployment("deployment-a").ListSkillAssignments(context.Background())
	if err != nil || len(skills) != 1 || skills[0].SubjectID != "team-a" {
		t.Fatalf("ListSkillAssignments() = %#v, %v", skills, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "credential_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "credential_id", "subject_type", "subject_id", "created_at"}).
			AddRow("assignment-c", "deployment-a", "credential-a", "role", "operator", now))
	credentials, err := store.Deployment("deployment-a").ListCredentialAssignments(context.Background())
	if err != nil || len(credentials) != 1 || credentials[0].CredentialID != "credential-a" {
		t.Fatalf("ListCredentialAssignments() = %#v, %v", credentials, err)
	}

	mock.ExpectQuery(`SELECT \* FROM "model_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "model_id", "subject_type", "subject_id", "created_at"}).
			AddRow("assignment-m", "deployment-a", "chat-a", "user", "user-a", now))
	models, err := store.Deployment("deployment-a").ListModelAssignments(context.Background())
	if err != nil || len(models) != 1 || models[0].ModelID != "chat-a" {
		t.Fatalf("ListModelAssignments() = %#v, %v", models, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "credential_assignments"`).
		WithArgs("assignment-new", "deployment-a", "credential-a", "user", "user-a", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	created, err := store.Deployment("deployment-a").CreateCredentialAssignment(context.Background(), CredentialAssignment{
		ID: "assignment-new", CredentialID: "credential-a", SubjectType: "user", SubjectID: "user-a",
	})
	if err != nil || created.DeploymentID != "deployment-a" {
		t.Fatalf("CreateCredentialAssignment() = %#v, %v", created, err)
	}
}

func TestTeamCreationAndDeletion(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "teams"`).
		WithArgs("deployment-a", "team-a", "Engineering", "Builders", false, true, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").CreateTeam(context.Background(), Team{
		ID: "team-a", Name: "Engineering", Description: "Builders",
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT \* FROM "teams" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "team-a", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "description", "built_in", "enabled", "created_at", "updated_at"}).
			AddRow("deployment-a", "team-a", "Engineering", "Builders", false, true, now, now))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "teams" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "team-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Deployment("deployment-a").DeleteTeam(context.Background(), "team-a"); err != nil {
		t.Fatal(err)
	}
}
