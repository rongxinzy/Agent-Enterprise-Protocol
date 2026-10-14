package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestTeamDefaultRoleValidation(t *testing.T) {
	application, _, adminToken := newStoreBackedHTTPApplication(t)
	handler := New(application).Handler()
	for _, body := range []string{`{"name":"Team","defaultRoleIds":["role","role"]}`, `{"name":"Team","defaultRoleIds":["bad role"]}`} {
		result := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams", body)
		if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "INVALID_ROLE") {
			t.Fatalf("invalid defaults = %d %s", result.Code, result.Body.String())
		}
	}
	tooMany := make([]string, 65)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("role-%d", i)
	}
	body, _ := json.Marshal(map[string]any{"name": "Team", "defaultRoleIds": tooMany})
	result := adminRequest(handler, adminToken, http.MethodPost, "/aep/v1/admin/teams", string(body))
	if result.Code != http.StatusBadRequest {
		t.Fatalf("too many defaults = %d", result.Code)
	}
}

func TestTeamDefaultRolesCreateAndPatchContract(t *testing.T) {
	for _, create := range []bool{true, false} {
		for _, unknown := range []bool{true, false} {
			t.Run(fmt.Sprintf("create=%t/unknown=%t", create, unknown), func(t *testing.T) {
				application, mock, token := newStoreBackedHTTPApplication(t)
				handler := New(application).Handler()
				if create {
					mock.ExpectQuery(`FROM "teams"`).WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(teamJoinColumns()))
				}
				mock.ExpectBegin()
				if create {
					mock.ExpectExec(`INSERT INTO "teams"`).WillReturnResult(sqlmock.NewResult(0, 1))
				} else {
					mock.ExpectExec(`UPDATE "teams" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
					expectTeamRecord(mock, "engineering", "Engineering", "", true, 1, time.Now())
				}
				count := 1
				if unknown {
					count = 0
				}
				mock.ExpectQuery(`SELECT count\(\*\) FROM "roles"`).WithArgs("deployment-a", "reader").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
				if unknown {
					mock.ExpectRollback()
				} else {
					mock.ExpectExec(`DELETE FROM "team_default_roles"`).WithArgs("deployment-a", "engineering").WillReturnResult(sqlmock.NewResult(0, 0))
					mock.ExpectExec(`INSERT INTO "team_default_roles"`).WithArgs("deployment-a", "engineering", "reader").WillReturnResult(sqlmock.NewResult(0, 1))
					if !create {
						mock.ExpectQuery(`FROM "teams"`).WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows([]string{"deployment_id", "id", "name", "enabled", "member_count", "default_role_ids"}).AddRow("deployment-a", "engineering", "Engineering", true, 1, `["reader"]`))
					}
					mock.ExpectCommit()
				}
				method, path := http.MethodPost, "/aep/v1/admin/teams"
				if !create {
					method, path = http.MethodPatch, path+"/engineering"
				}
				response := adminRequest(handler, token, method, path, `{"name":"Engineering","defaultRoleIds":["reader"]}`)
				if unknown {
					if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_ROLE") {
						t.Fatalf("invalid defaults = %d %s", response.Code, response.Body.String())
					}
				} else if (response.Code != http.StatusOK && response.Code != http.StatusCreated) || !strings.Contains(response.Body.String(), `"defaultRoleIds":["reader"]`) {
					t.Fatalf("persisted defaults = %d %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestReferencedTeamDefaultRoleCannotBeDeleted(t *testing.T) {
	application, mock, token := newStoreBackedHTTPApplication(t)
	now := time.Now()
	mock.ExpectQuery(`SELECT \* FROM "roles"`).WithArgs("deployment-a", "reader", 1).WillReturnRows(sqlmock.NewRows(roleHTTPColumns()).AddRow("deployment-a", "reader", "Reader", "", false, true, now, now))
	mock.ExpectBegin()
	// Deleting a subject also clears its polymorphic grants first; the reference
	// that blocks the role delete is the team's default-role foreign key.
	expectSubjectGrantCleanup(mock, "role", "reader")
	mock.ExpectExec(`DELETE FROM "roles"`).WithArgs("deployment-a", "reader").WillReturnError(&pgconn.PgError{Code: "23503"})
	mock.ExpectRollback()
	result := adminRequest(New(application).Handler(), token, http.MethodDelete, "/aep/v1/admin/roles/reader", "")
	if result.Code != http.StatusConflict || !strings.Contains(result.Body.String(), "ROLE_IN_USE") {
		t.Fatalf("referenced role = %d %s", result.Code, result.Body.String())
	}
}

func TestDelegatedTeamDefaultsRejectPrivilegeEscalation(t *testing.T) {
	application, mock, _ := newStoreBackedHTTPApplication(t)
	server := &Server{app: application}
	token, _, err := application.Tokens.IssueWithDeploymentSession("delegated-admin", "deployment-a", "session-delegated", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := application.Tokens.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/aep/v1/admin/teams/engineering", nil)
	request = request.WithContext(context.WithValue(request.Context(), claimsContextKey, claims))
	now := time.Now().UTC()
	expectRoleRecord(mock, "privileged", "Privileged", "", true, []string{"roles.write"}, now)
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM role_permissions AS rp JOIN user_effective_roles AS urb`).WithArgs("deployment-a", "delegated-admin").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("teams.write"))
	defaults := []string{"privileged"}
	response := httptest.NewRecorder()
	if server.authorizeTeamDefaults(response, request, &defaults) || response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ROLE_GRANT_FORBIDDEN") {
		t.Fatalf("default role escalation = %d %s", response.Code, response.Body.String())
	}

	// Even an operator already in the team must not grant a privileged default
	// to another member using the otherwise valid team-membership path.
	mock.ExpectQuery(`SELECT "team_id" FROM "user_team_bindings"`).WithArgs("deployment-a", "delegated-admin").WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow("engineering"))
	mock.ExpectQuery(`SELECT teams\.\*, COUNT.*FROM "teams"`).WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(append(teamJoinColumns(), "default_role_ids")).AddRow("deployment-a", "engineering", "Engineering", "", false, true, nil, "/engineering", 0, now, now, 1, `["privileged"]`))
	expectRoleRecord(mock, "privileged", "Privileged", "", true, []string{"roles.write"}, now)
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM role_permissions AS rp JOIN user_effective_roles AS urb`).WithArgs("deployment-a", "delegated-admin").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("teams.write"))
	response = httptest.NewRecorder()
	if server.authorizeTeamGrant(response, request, []string{"engineering"}) || response.Code != http.StatusForbidden {
		t.Fatalf("membership escalation = %d %s", response.Code, response.Body.String())
	}
}

func TestTeamReenableChecksExistingDefaultRoles(t *testing.T) {
	application, mock, _ := newStoreBackedHTTPApplication(t)
	server := &Server{app: application}
	token, _, err := application.Tokens.IssueWithDeploymentSession("operator", "deployment-a", "session-a", false, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := application.Tokens.ParseAccess(token)
	if err != nil {
		t.Fatal(err)
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("teamId", "engineering")
	request := httptest.NewRequest(http.MethodPatch, "/aep/v1/admin/teams/engineering", strings.NewReader(`{"enabled":true}`))
	request = request.WithContext(context.WithValue(context.WithValue(request.Context(), claimsContextKey, claims), chi.RouteCtxKey, route))
	now := time.Now()
	mock.ExpectQuery(`FROM "teams"`).WithArgs("deployment-a").WillReturnRows(sqlmock.NewRows(append(teamJoinColumns(), "default_role_ids")).AddRow("deployment-a", "engineering", "Engineering", "", false, false, nil, "/engineering", 0, now, now, 1, `["privileged"]`))
	expectRoleRecord(mock, "privileged", "Privileged", "", true, []string{"roles.write"}, now)
	mock.ExpectQuery(`SELECT DISTINCT rp.permission_id FROM role_permissions AS rp`).WithArgs("deployment-a", "operator").WillReturnRows(sqlmock.NewRows([]string{"permission_id"}).AddRow("teams.write"))
	response := httptest.NewRecorder()
	server.updateTeam(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "ROLE_GRANT_FORBIDDEN") {
		t.Fatalf("reenable escalated defaults = %d %s", response.Code, response.Body.String())
	}
}
