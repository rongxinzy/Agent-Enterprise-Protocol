package repository

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestCreateTeamDefaultsAtomicAndTenantScoped(t *testing.T) {
	for _, failure := range []string{"", "insert", "unknown", "lookup", "delete", "bindings"} {
		t.Run(failure, func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectBegin()
			insert := mock.ExpectExec(`INSERT INTO "teams"`)
			if failure == "insert" {
				insert.WillReturnError(errors.New("insert failed"))
			} else {
				insert.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if failure != "insert" {
				count := mock.ExpectQuery(`SELECT count\(\*\) FROM "roles" WHERE deployment_id = \$1 AND id IN \(\$2,\$3\)`).WithArgs("deployment-a", "reader", "observer")
				if failure == "lookup" {
					count.WillReturnError(errors.New("lookup failed"))
				} else {
					number := 2
					if failure == "unknown" {
						number = 1
					}
					count.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(number))
				}
				if failure != "lookup" && failure != "unknown" {
					remove := mock.ExpectExec(`DELETE FROM "team_default_roles" WHERE deployment_id = \$1 AND team_id = \$2`).WithArgs("deployment-a", "engineering")
					if failure == "delete" {
						remove.WillReturnError(errors.New("delete failed"))
					} else {
						remove.WillReturnResult(sqlmock.NewResult(0, 0))
						bindings := mock.ExpectExec(`INSERT INTO "team_default_roles"`).WithArgs("deployment-a", "engineering", "reader", "deployment-a", "engineering", "observer")
						if failure == "bindings" {
							bindings.WillReturnError(errors.New("bindings failed"))
						} else {
							bindings.WillReturnResult(sqlmock.NewResult(0, 2))
						}
					}
				}
			}
			if failure == "" {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := store.Deployment("deployment-a").CreateTeam(context.Background(), Team{ID: "engineering", Name: "Engineering"}, []string{"reader", "observer", "reader"})
			if (err != nil) != (failure != "") {
				t.Fatalf("CreateTeam defaults = %v", err)
			}
			if failure == "unknown" && !errors.Is(err, ErrUnknownRole) {
				t.Fatalf("unknown/foreign tenant role = %v", err)
			}
		})
	}
}

func TestUpdateTeamDefaultsPreservesExplicitBindings(t *testing.T) {
	for _, defaults := range [][]string{{"reader"}, {}} {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "teams" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
		rows := func(ids string) *sqlmock.Rows {
			return sqlmock.NewRows([]string{"deployment_id", "id", "name", "enabled", "member_count", "default_role_ids"}).AddRow("deployment-a", "engineering", "Engineering", true, 2, ids)
		}
		mock.ExpectQuery(`SELECT teams\.\*, COUNT.*FROM "teams"`).WithArgs("deployment-a").WillReturnRows(rows(`["observer"]`))
		if len(defaults) > 0 {
			mock.ExpectQuery(`SELECT count\(\*\) FROM "roles"`).WithArgs("deployment-a", "reader").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		}
		mock.ExpectExec(`DELETE FROM "team_default_roles"`).WithArgs("deployment-a", "engineering").WillReturnResult(sqlmock.NewResult(0, 1))
		jsonIDs := "[]"
		if len(defaults) > 0 {
			mock.ExpectExec(`INSERT INTO "team_default_roles"`).WithArgs("deployment-a", "engineering", "reader").WillReturnResult(sqlmock.NewResult(0, 1))
			jsonIDs = `["reader"]`
		}
		mock.ExpectQuery(`SELECT teams\.\*, COUNT.*FROM "teams"`).WithArgs("deployment-a").WillReturnRows(rows(jsonIDs))
		mock.ExpectCommit()
		result, err := store.Deployment("deployment-a").UpdateTeam(context.Background(), "engineering", nil, nil, nil, defaults)
		if err != nil || len(result.DefaultRoleIDs) != len(defaults) {
			t.Fatalf("UpdateTeam = %#v, %v", result, err)
		}
		// No INSERT/DELETE on user_role_bindings is expected: inherited role
		// changes must never mutate independent explicit user grants.
	}
}
