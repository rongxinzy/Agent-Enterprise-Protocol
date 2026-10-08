package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/app"
)

func modelHTTPColumns() []string {
	return []string{
		"deployment_id", "id", "display_name", "source_type", "protocol", "endpoint", "upstream_model", "local_model_ref", "credential_id",
		"capabilities", "reasoning_compatibility", "context_window", "is_default", "enabled", "health_status", "health_checked_at", "health_detail", "created_at", "updated_at",
	}
}

func modelRuntimeColumns() []string {
	return []string{
		"id", "display_name", "source_type", "protocol", "endpoint", "upstream_model", "local_model_ref", "credential_id",
		"capabilities", "reasoning_compatibility", "context_window", "is_default", "enabled", "health_status", "health_checked_at", "health_detail", "created_at", "updated_at",
	}
}

func attachRuntimeDatabase(t *testing.T, application *app.App) pgxmock.PgxPoolIface {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	application.SetRuntimeDatabase(pool)
	t.Cleanup(func() {
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		pool.Close()
	})
	return pool
}

func TestAdminModelLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	handler := New(application).Handler()
	now := time.Now().UTC()
	reasoning := []byte(`{"thinkingFormat":"deepseek","supportsReasoningEffort":true,"requiresReasoningContentOnAssistantMessages":true}`)

	// The model identifier is generated from the display name: the existence
	// probe runs on the store (sqlmock) before the runtime transaction.
	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "enterprise-chat").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM credentials`).WithArgs("deployment-a", "credential-a").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectExec(`UPDATE models SET is_default=false`).WithArgs("deployment-a").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectQuery(`INSERT INTO models`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(modelRuntimeColumns()).AddRow(
			"enterprise-chat", "Enterprise Chat", "gateway", "openai-compatible", "http://gateway/v1", "deepseek-chat", nil, "credential-a",
			[]string{"reasoning", "text"}, reasoning, int64(32768), true, true, "healthy", now, "upstream catalog lists deepseek-chat", now, now,
		))
	pool.ExpectCommit()
	created := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/models", `{
		"displayName":"Enterprise Chat","sourceType":"gateway","protocol":"openai-compatible",
		"endpoint":"http://gateway/v1","upstreamModel":"deepseek-chat","credentialId":"credential-a",
		"capabilities":[" text ","reasoning","text"],
		"reasoningCompatibility":{"thinkingFormat":"deepseek","supportsReasoningEffort":true,"requiresReasoningContentOnAssistantMessages":true},
		"contextWindow":32768,"isDefault":true,"enabled":true
	}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"enterprise-chat"`) || !strings.Contains(created.Body.String(), `"capabilities":["reasoning","text"]`) || !strings.Contains(created.Body.String(), `"thinkingFormat":"deepseek"`) {
		t.Fatalf("create model = %d %s", created.Code, created.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 AND id = \$2 LIMIT \$3`).
		WithArgs("deployment-a", "enterprise-chat", 1).
		WillReturnRows(sqlmock.NewRows(modelHTTPColumns()).AddRow(
			"deployment-a", "enterprise-chat", "Enterprise Chat", "gateway", "openai-compatible", "http://gateway/v1", "deepseek-chat", nil, "credential-a",
			`{reasoning,text}`, reasoning, 32768, true, true, "credential_invalid", now, "HTTP 401: unauthorized", now, now,
		))
	got := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/models/enterprise-chat", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"credentialId":"credential-a"`) || !strings.Contains(got.Body.String(), `"contextWindow":32768`) ||
		!strings.Contains(got.Body.String(), `"healthStatus":"credential_invalid"`) || !strings.Contains(got.Body.String(), `"healthDetail":"HTTP 401: unauthorized"`) {
		t.Fatalf("get model = %d %s", got.Code, got.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM credentials`).WithArgs("deployment-a", "credential-b").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectExec(`UPDATE models SET is_default=false`).WithArgs("deployment-a", "enterprise-chat").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectQuery(`UPDATE models SET`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(modelRuntimeColumns()).AddRow(
			"enterprise-chat", "Enterprise Chat 2", "gateway", "openai-compatible", "http://gateway/v2", "deepseek-reasoner", nil, "credential-b",
			[]string{"reasoning", "streaming", "text"}, reasoning, int64(65536), true, true, "healthy", now, nil, now, now,
		))
	pool.ExpectCommit()
	updated := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/models/enterprise-chat", `{
		"displayName":"Enterprise Chat 2","endpoint":"http://gateway/v2","upstreamModel":"deepseek-reasoner",
		"credentialId":"credential-b","capabilities":["streaming","text","reasoning"],"contextWindow":65536,"isDefault":true
	}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"displayName":"Enterprise Chat 2"`) || !strings.Contains(updated.Body.String(), `"credentialId":"credential-b"`) || !strings.Contains(updated.Body.String(), `"contextWindow":65536`) {
		t.Fatalf("update model = %d %s", updated.Code, updated.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "model_assignments" WHERE deployment_id = \$1 ORDER BY created_at, id`).
		WithArgs("deployment-a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_id", "model_id", "subject_type", "subject_id", "created_at"}).
			AddRow("assignment-a", "deployment-a", "enterprise-chat", "team", "engineering", now))
	listedAssignments := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/model-assignments", "")
	if listedAssignments.Code != http.StatusOK || !strings.Contains(listedAssignments.Body.String(), `"resourceId":"enterprise-chat"`) || !strings.Contains(listedAssignments.Body.String(), `"type":"team"`) {
		t.Fatalf("list model assignments = %d %s", listedAssignments.Code, listedAssignments.Body.String())
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "enterprise-chat").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "model_assignments"`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	assigned := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/model-assignments", `{"modelId":"enterprise-chat","subject":{"type":"role","id":"developer"}}`)
	if assigned.Code != http.StatusCreated || !strings.Contains(assigned.Body.String(), `"resourceType":"model"`) || !strings.Contains(assigned.Body.String(), `"type":"role"`) {
		t.Fatalf("create model assignment = %d %s", assigned.Code, assigned.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "model_assignments" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "assignment-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	revoked := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/model-assignments/assignment-a", "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("delete model assignment = %d %s", revoked.Code, revoked.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "enterprise-chat").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	deleted := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/models/enterprise-chat", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete model = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestUserModelCatalogUsesAssignmentsAndHidesCredential(t *testing.T) {
	application, mock, _ := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	userToken, _, err := application.Tokens.IssueWithDeploymentSession("user-a", "deployment-a", "session-user", false, false, []string{"member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(application).Handler()
	now := time.Now().UTC()

	expectHTTPModelScopes(pool, "deployment-a", "user-a", "chat-a")
	mock.ExpectQuery(`SELECT \* FROM "models" WHERE deployment_id = \$1 AND enabled = \$2 AND id IN \(\$3\) ORDER BY is_default DESC, id`).
		WithArgs("deployment-a", true, "chat-a").
		WillReturnRows(sqlmock.NewRows(modelHTTPColumns()).AddRow(
			"deployment-a", "chat-a", "Enterprise Chat", "gateway", "openai-compatible", "http://gateway/v1", "deepseek-chat", nil, "credential-a",
			`{text,reasoning}`, []byte(`{"thinkingFormat":"deepseek","supportsReasoningEffort":true,"requiresReasoningContentOnAssistantMessages":true}`), 32768, true, true, "unknown", nil, nil, now, now,
		))
	response := userRequest(handler, userToken, http.MethodGet, "/aep/v1/user/models", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"chat-a"`) || !strings.Contains(response.Body.String(), `"reasoningCompatibility"`) || strings.Contains(response.Body.String(), "credentialId") || strings.Contains(response.Body.String(), "credential-a") {
		t.Fatalf("user model catalog = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminModelAnthropicPatchGuard(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	handler := New(application).Handler()
	now := time.Now().UTC()
	reasoning := []byte(`{"thinkingFormat":"deepseek","supportsReasoningEffort":true,"requiresReasoningContentOnAssistantMessages":true}`)

	// PATCH reasoningCompatibility onto an anthropic model: the UPDATE row
	// comes back anthropic, so the post-update guard must reject and roll
	// back even though the patch payload itself is well-formed.
	pool.ExpectBegin()
	pool.ExpectQuery(`UPDATE models SET`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(modelRuntimeColumns()).AddRow(
			"bench-anthropic", "Bench", "gateway", "anthropic", "https://open.bigmodel.cn/api/anthropic", "glm-5.3-flash", nil, "credential-bigmodel",
			[]string{"text"}, reasoning, nil, false, true, "unknown", nil, nil, now, now,
		))
	pool.ExpectRollback()
	rejected := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/models/bench-anthropic", `{"reasoningCompatibility":{"thinkingFormat":"deepseek","supportsReasoningEffort":true,"requiresReasoningContentOnAssistantMessages":true}}`)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), `"code":"INVALID_MODEL"`) {
		t.Fatalf("anthropic reasoning patch = %d %s", rejected.Code, rejected.Body.String())
	}

	// A relative endpoint patched onto an anthropic model is equally stranded.
	pool.ExpectBegin()
	pool.ExpectQuery(`UPDATE models SET`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(modelRuntimeColumns()).AddRow(
			"bench-anthropic", "Bench", "gateway", "anthropic", "/v1", "glm-5.3-flash", nil, nil,
			[]string{"text"}, nil, nil, false, true, "unknown", nil, nil, now, now,
		))
	pool.ExpectRollback()
	broken := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/models/bench-anthropic", `{"endpoint":"/v1"}`)
	if broken.Code != http.StatusBadRequest || !strings.Contains(broken.Body.String(), `"code":"INVALID_MODEL"`) {
		t.Fatalf("anthropic relative endpoint patch = %d %s", broken.Code, broken.Body.String())
	}

	// The same endpoint on an openai-compatible model stays out of the guard.
	pool.ExpectBegin()
	pool.ExpectQuery(`UPDATE models SET`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(modelRuntimeColumns()).AddRow(
			"chat-a", "Chat", "gateway", "openai-compatible", "/v1", "deepseek-chat", nil, nil,
			[]string{"text"}, nil, nil, false, true, "unknown", nil, nil, now, now,
		))
	pool.ExpectCommit()
	allowed := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/models/chat-a", `{"endpoint":"/v1"}`)
	if allowed.Code != http.StatusOK {
		t.Fatalf("openai-compatible endpoint patch = %d %s", allowed.Code, allowed.Body.String())
	}
}

func TestAdminModelErrorMappings(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	pool := attachRuntimeDatabase(t, application)
	handler := New(application).Handler()
	validModel := `{"displayName":"Chat","sourceType":"gateway","protocol":"openai-compatible","credentialId":"credential-a","capabilities":["text"],"isDefault":false,"enabled":true}`

	// Identifier probe ("Chat" -> "chat") on the store before the runtime tx.
	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "chat").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM credentials`).WithArgs("deployment-a", "credential-a").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	pool.ExpectRollback()
	missingCredential := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/models", validModel)
	if missingCredential.Code != http.StatusNotFound || !strings.Contains(missingCredential.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing model credential = %d %s", missingCredential.Code, missingCredential.Body.String())
	}

	// Unique-violation fallback stays covered for the concurrent-create race.
	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "chat").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	pool.ExpectBegin()
	pool.ExpectQuery(`INSERT INTO models`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnError(&pgconn.PgError{Code: "23505"})
	pool.ExpectRollback()
	duplicate := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/models", strings.Replace(validModel, `"credentialId":"credential-a",`, "", 1))
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"MODEL_EXISTS"`) {
		t.Fatalf("duplicate model = %d %s", duplicate.Code, duplicate.Body.String())
	}

	pool.ExpectBegin()
	pool.ExpectQuery(`UPDATE models SET`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	pool.ExpectRollback()
	missingModel := adminRequest(handler, adminToken, http.MethodPatch, "/aep/v1/admin/models/missing", `{"enabled":false}`)
	if missingModel.Code != http.StatusNotFound || !strings.Contains(missingModel.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("update missing model = %d %s", missingModel.Code, missingModel.Body.String())
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "missing").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	missingAssignmentModel := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/model-assignments", `{"modelId":"missing","subject":{"type":"user","id":"user-a"}}`)
	if missingAssignmentModel.Code != http.StatusNotFound || !strings.Contains(missingAssignmentModel.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("assign missing model = %d %s", missingAssignmentModel.Code, missingAssignmentModel.Body.String())
	}

	mock.ExpectQuery(`SELECT count\(\*\) FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "chat-a").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "model_assignments"`).WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()
	duplicateAssignment := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/model-assignments", `{"modelId":"chat-a","subject":{"type":"user","id":"user-a"}}`)
	if duplicateAssignment.Code != http.StatusConflict || !strings.Contains(duplicateAssignment.Body.String(), `"code":"ASSIGNMENT_EXISTS"`) {
		t.Fatalf("duplicate model assignment = %d %s", duplicateAssignment.Code, duplicateAssignment.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "models" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "missing").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	deleteMissing := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/models/missing", "")
	if deleteMissing.Code != http.StatusNotFound || !strings.Contains(deleteMissing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("delete missing model = %d %s", deleteMissing.Code, deleteMissing.Body.String())
	}

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "model_assignments" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "missing").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	deleteMissingAssignment := adminRequest(handler, adminToken, http.MethodDelete, "/aep/v1/admin/model-assignments/missing", "")
	if deleteMissingAssignment.Code != http.StatusNotFound || !strings.Contains(deleteMissingAssignment.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("delete missing model assignment = %d %s", deleteMissingAssignment.Code, deleteMissingAssignment.Body.String())
	}
}
