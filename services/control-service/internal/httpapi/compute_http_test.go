package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func writeTierCatalog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tiers.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestComputeTierCatalogReadsDeploymentCatalog(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	application.Config.ComputeTiersFile = writeTierCatalog(t, `{"items":[{"name":"standard","description":"Default tier","cpuRequest":"250m","cpuLimit":"1000m","memoryRequest":"512Mi","memoryLimit":"1Gi"},{"name":"performance","cpuRequest":"1","cpuLimit":"2","memoryRequest":"2Gi","memoryLimit":"4Gi"}]}`)
	handler := New(application).Handler()

	response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/compute/tiers", "")
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(body, `"name":"standard"`) ||
		!strings.Contains(body, `"description":"Default tier"`) ||
		!strings.Contains(body, `"cpuRequest":"250m"`) ||
		!strings.Contains(body, `"memoryLimit":"1Gi"`) ||
		strings.Count(body, `"source":"deployment"`) != 2 {
		t.Fatalf("tier catalog = %d %s", response.Code, body)
	}
}

func TestComputeTierCatalogEmptyWithoutConfiguration(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/compute/tiers", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `{"items":[]}`) {
		t.Fatalf("unconfigured catalog = %d %s", response.Code, response.Body.String())
	}
}

func TestComputeTierCatalogFailsClosedWhenUnreadable(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":  `{"items":[`,
		"incomplete": `{"items":[{"name":"standard","cpuRequest":"250m","cpuLimit":"","memoryRequest":"512Mi","memoryLimit":"1Gi"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			application, _, adminToken := newStoreBackedHTTPApplication(t)
			application.Config.ComputeTiersFile = writeTierCatalog(t, content)
			handler := New(application).Handler()

			response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/compute/tiers", "")
			if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"COMPUTE_TIERS_UNAVAILABLE"`) {
				t.Fatalf("broken catalog = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func agentDirectoryRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "username", "display_name", "status", "kind", "display_title", "description", "avatar_object_key", "home_team_id", "prompt_skill_id", "ephemeral", "expires_at", "created_at", "updated_at", "last_seen_at"}).
		AddRow("agent-1", "agent-one", "Agent One", "active", "agent", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).
		AddRow("agent-2", "agent-two", "Agent Two", "active", "agent", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func TestComputeAssignmentListResolvesOverrides(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	application.Config.ComputeDefaultTier = "standard"
	handler := New(application).Handler()

	mock.ExpectQuery(`SELECT u.id, u.username, u.display_name, u.status, u.kind,`).
		WithArgs("deployment-a", "", "", false, 200).
		WillReturnRows(agentDirectoryRows())
	updatedAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM "agent_compute_assignments" WHERE deployment_id = \$1 AND agent_id IN \(\$2,\$3\)`).
		WithArgs("deployment-a", "agent-1", "agent-2").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "agent_id", "tier", "updated_by", "updated_at"}).
			AddRow("deployment-a", "agent-1", "performance", "admin-user", updatedAt))

	response := adminRequest(handler, adminToken, http.MethodGet, "/aep/v1/admin/compute/assignments", "")
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(body, `"agentId":"agent-1","agentName":"Agent One","tier":{"override":"performance","effective":"performance","source":"override"},"updatedAt":"2026-10-11T12:00:00Z","updatedBy":"admin-user"`) ||
		!strings.Contains(body, `"agentId":"agent-2","agentName":"Agent Two","tier":{"override":null,"effective":"standard","source":"default"}`) {
		t.Fatalf("assignment list = %d %s", response.Code, body)
	}
}

func userRow(kind string) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows([]string{"deployment_id", "id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "kind", "created_at", "updated_at"}).
		AddRow("deployment-a", "agent-1", "agent-one", "Agent One", nil, "hash", "active", false, false, kind, now, now)
}

func TestComputeAssignmentWriteLifecycle(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	application.Config.ComputeDefaultTier = "standard"
	application.Config.ComputeTiersFile = writeTierCatalog(t, `{"items":[{"name":"standard","cpuRequest":"250m","cpuLimit":"1000m","memoryRequest":"512Mi","memoryLimit":"1Gi"},{"name":"performance","cpuRequest":"1","cpuLimit":"2","memoryRequest":"2Gi","memoryLimit":"4Gi"}]}`)
	handler := New(application).Handler()

	// Store a tier override and echo it back with the operator attribution.
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "agent-1", 1).WillReturnRows(userRow("agent"))
	mock.ExpectExec(`INSERT INTO agent_compute_assignments`).WillReturnResult(sqlmock.NewResult(1, 1))
	storedAt := time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM "agent_compute_assignments" WHERE deployment_id = \$1 AND agent_id IN \(\$2\)`).
		WithArgs("deployment-a", "agent-1").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "agent_id", "tier", "updated_by", "updated_at"}).
			AddRow("deployment-a", "agent-1", "performance", "admin-user", storedAt))

	updated := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/compute/assignments/agent-1", `{"tier":"performance"}`)
	if updated.Code != http.StatusOK ||
		!strings.Contains(updated.Body.String(), `"tier":{"override":"performance","effective":"performance","source":"override"}`) ||
		!strings.Contains(updated.Body.String(), `"updatedBy":"admin-user"`) {
		t.Fatalf("assignment update = %d %s", updated.Code, updated.Body.String())
	}

	// Null clears the override so the deployment default applies again.
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "agent-1", 1).WillReturnRows(userRow("agent"))
	mock.ExpectExec(`DELETE FROM agent_compute_assignments WHERE deployment_id = \$1 AND agent_id = \$2`).
		WithArgs("deployment-a", "agent-1").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT \* FROM "agent_compute_assignments" WHERE deployment_id = \$1 AND agent_id IN \(\$2\)`).
		WithArgs("deployment-a", "agent-1").
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "agent_id", "tier", "updated_by", "updated_at"}))

	cleared := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/compute/assignments/agent-1", `{"tier":null}`)
	if cleared.Code != http.StatusOK ||
		!strings.Contains(cleared.Body.String(), `"tier":{"override":null,"effective":"standard","source":"default"}`) {
		t.Fatalf("assignment clear = %d %s", cleared.Code, cleared.Body.String())
	}
}

func TestComputeAssignmentWriteRejectsUnknownAgent(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "ghost", 1).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "username", "display_name", "email", "password_hash", "status", "require_password_change", "is_admin", "kind", "created_at", "updated_at"}))

	missing := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/compute/assignments/ghost", `{"tier":"standard"}`)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("missing agent = %d %s", missing.Code, missing.Body.String())
	}

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "human-1", 1).WillReturnRows(userRow("human"))
	human := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/compute/assignments/human-1", `{"tier":"standard"}`)
	if human.Code != http.StatusNotFound || !strings.Contains(human.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
		t.Fatalf("human account = %d %s", human.Code, human.Body.String())
	}
}

func TestComputeAssignmentWriteRejectsUnknownTier(t *testing.T) {
	application, mock, adminToken := newStoreBackedHTTPApplication(t)
	application.Config.ComputeTiersFile = writeTierCatalog(t, `{"items":[{"name":"standard","cpuRequest":"250m","cpuLimit":"1000m","memoryRequest":"512Mi","memoryLimit":"1Gi"}]}`)
	handler := New(application).Handler()

	mock.ExpectQuery(`SELECT \* FROM "users" WHERE deployment_id = \$1 AND id = \$2`).
		WithArgs("deployment-a", "agent-1", 1).WillReturnRows(userRow("agent"))

	rejected := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/compute/assignments/agent-1", `{"tier":"platinum"}`)
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), `"code":"INVALID_COMPUTE_TIER"`) {
		t.Fatalf("unknown tier = %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestComputeAssignmentWriteValidatesBodyShape(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()

	for name, body := range map[string]string{
		"missing tier":    `{}`,
		"unknown field":   `{"tier":"standard","extra":true}`,
		"empty tier":      `{"tier":" "}`,
		"non-string tier": `{"tier":42}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := adminRequest(handler, adminToken, http.MethodPut, "/aep/v1/admin/compute/assignments/agent-1", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", name, response.Code, response.Body.String())
			}
		})
	}
}

func TestComputeRoutesRequireTheirPermissions(t *testing.T) {
	application, pool, _, _ := newRuntimeHTTPApplication(t)
	handler := New(application).Handler()
	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-user", "deployment-a", "session-delegated", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	pool.ExpectQuery(`SELECT EXISTS \(`).
		WithArgs("deployment-a", "delegated-user", "deployment.read").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	read := adminRequest(handler, token, http.MethodGet, "/aep/v1/admin/compute/tiers", "")
	if read.Code != http.StatusForbidden || !strings.Contains(read.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("tiers without deployment.read = %d %s", read.Code, read.Body.String())
	}

	pool.ExpectQuery(`SELECT EXISTS \(`).
		WithArgs("deployment-a", "delegated-user", "agents.compute.write").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	write := adminRequest(handler, token, http.MethodPut, "/aep/v1/admin/compute/assignments/agent-1", `{"tier":"standard"}`)
	if write.Code != http.StatusForbidden || !strings.Contains(write.Body.String(), `"code":"ACCESS_DENIED"`) {
		t.Fatalf("assignment without agents.compute.write = %d %s", write.Code, write.Body.String())
	}
}

func TestComputeAssignmentAuditTarget(t *testing.T) {
	action, resourceType, resourceID := adminAuditTarget(http.MethodPut, "/aep/v1/admin/compute/assignments/agent-9")
	if action != "update" || resourceType != "compute_assignment" || resourceID != "agent-9" {
		t.Fatalf("audit target = %s %s %s", action, resourceType, resourceID)
	}
}
