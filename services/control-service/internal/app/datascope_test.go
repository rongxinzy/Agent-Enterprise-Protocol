package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/config"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
)

var errDataScopeRead = errors.New("datascope read failed")

// DataScopeContext assembles a user's retrieval context (own teams + managed
// subtrees + explicit grants/denies). These tests drive the three SQL reads
// (roles via GORM, teams and rules via the pool) through mocks.
func newDataScopeApp(t *testing.T) (*App, sqlmock.Sqlmock, pgxmock.PgxPoolIface) {
	t.Helper()
	sqlDB, sqlMock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New(): %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(): %v", err)
	}

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool(): %v", err)
	}
	t.Cleanup(pool.Close)

	application := &App{Store: repository.New(ormDB)}
	application.SetRuntimeDatabase(pool)
	return application, sqlMock, pool
}

func TestDataScopeContext(t *testing.T) {
	t.Run("own team plus managed subtree plus grant", func(t *testing.T) {
		application, sqlMock, pool := newDataScopeApp(t)

		sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
			WithArgs("deployment-a", "user-a").
			WillReturnRows(sqlmock.NewRows([]string{"role_id"}).AddRow("manager"))

		teamRows := pgxmock.NewRows([]string{"id", "parent", "path"}).
			AddRow("engineering", "", "/engineering").
			AddRow("backend", "engineering", "/engineering/backend")
		pool.ExpectQuery(`JOIN user_team_bindings`).WithArgs("deployment-a", "user-a").WillReturnRows(teamRows)

		treeRows := pgxmock.NewRows([]string{"id", "parent", "path"}).
			AddRow("engineering", "", "/engineering").
			AddRow("backend", "engineering", "/engineering/backend").
			AddRow("sales", "", "/sales")
		pool.ExpectQuery(`FROM teams WHERE deployment_id`).WithArgs("deployment-a").WillReturnRows(treeRows)

		ruleRows := pgxmock.NewRows([]string{"id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at"}).
			AddRow("r1", "management_scope", "user", "user-a", "skill", "*", nil, nil)
		pool.ExpectQuery(`FROM data_scope_rules`).WithArgs("deployment-a", "user-a", []string{"manager"}, []string{"engineering", "backend"}).WillReturnRows(ruleRows)

		resolved, err := application.DataScopeContext(context.Background(), "deployment-a", "user-a")
		if err != nil {
			t.Fatalf("DataScopeContext(): %v", err)
		}
		if !contains(resolved.OwnTeamIDs, "engineering") {
			t.Fatalf("OwnTeamIDs = %#v, want engineering", resolved.OwnTeamIDs)
		}
		if !contains(resolved.OrgScope, "engineering") || !contains(resolved.OrgScope, "backend") {
			t.Fatalf("OrgScope = %#v, want engineering and backend", resolved.OrgScope)
		}
	})

	t.Run("role failure surfaces", func(t *testing.T) {
		application, sqlMock, _ := newDataScopeApp(t)
		sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
			WillReturnError(errDataScopeRead)
		if _, err := application.DataScopeContext(context.Background(), "deployment-a", "user-a"); err == nil {
			t.Fatal("role read failure must surface")
		}
	})

	t.Run("team read failure surfaces", func(t *testing.T) {
		application, sqlMock, pool := newDataScopeApp(t)
		sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
			WillReturnRows(sqlmock.NewRows([]string{"role_id"}))
		pool.ExpectQuery(`JOIN user_team_bindings`).WillReturnError(errDataScopeRead)
		if _, err := application.DataScopeContext(context.Background(), "deployment-a", "user-a"); err == nil {
			t.Fatal("team read failure must surface")
		}
	})

	t.Run("rules read failure surfaces", func(t *testing.T) {
		application, sqlMock, pool := newDataScopeApp(t)
		sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
			WillReturnRows(sqlmock.NewRows([]string{"role_id"}))
		pool.ExpectQuery(`JOIN user_team_bindings`).
			WillReturnRows(pgxmock.NewRows([]string{"id", "parent", "path"}))
		pool.ExpectQuery(`FROM teams WHERE deployment_id`).
			WillReturnRows(pgxmock.NewRows([]string{"id", "parent", "path"}))
		pool.ExpectQuery(`FROM data_scope_rules`).WillReturnError(errDataScopeRead)
		if _, err := application.DataScopeContext(context.Background(), "deployment-a", "user-a"); err == nil {
			t.Fatal("rules read failure must surface")
		}
	})
}

func TestDataScopeRuleWindow(t *testing.T) {
	// The rule read filters by time window at the SQL level; the Go side just
	// scans. Guard the scan shapes with a full row including timestamps.
	application, sqlMock, pool := newDataScopeApp(t)
	sqlMock.ExpectQuery(`SELECT "role_id" FROM "user_role_bindings"`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(sqlmock.NewRows([]string{"role_id"}))
	pool.ExpectQuery(`JOIN user_team_bindings`).
		WithArgs("deployment-a", "user-a").
		WillReturnRows(pgxmock.NewRows([]string{"id", "parent", "path"}))
	pool.ExpectQuery(`FROM teams WHERE deployment_id`).
		WithArgs("deployment-a").
		WillReturnRows(pgxmock.NewRows([]string{"id", "parent", "path"}))
	// pgxmock cannot scan a time struct into a *time.Time destination, so the
	// window columns come through as NULL; the SQL-level window filter is
	// what actually enforces timing, the scan only carries the pointers.
	pool.ExpectQuery(`FROM data_scope_rules`).
		WithArgs("deployment-a", "user-a", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "rule_kind", "subject_type", "subject_id", "resource_kind", "resource_id", "starts_at", "expires_at"}).
			AddRow("r2", "exception_grant", "role", "manager", "model", "bench-glm", nil, nil))

	if _, err := application.DataScopeContext(context.Background(), "deployment-a", "user-a"); err != nil {
		t.Fatalf("DataScopeContext(): %v", err)
	}
}

func TestRuleAppliesBySubjectType(t *testing.T) {
	subject := ScopeSubject{UserID: "u1", RoleIDs: []string{"manager"}, TeamIDs: []string{"rd"}}
	cases := []struct {
		rule ScopeRule
		want bool
	}{
		{ScopeRule{SubjectType: "user", SubjectID: "u1"}, true},
		{ScopeRule{SubjectType: "user", SubjectID: "u2"}, false},
		{ScopeRule{SubjectType: "role", SubjectID: "manager"}, true},
		{ScopeRule{SubjectType: "role", SubjectID: "auditor"}, false},
		{ScopeRule{SubjectType: "team", SubjectID: "rd"}, true},
		{ScopeRule{SubjectType: "team", SubjectID: "sales"}, false},
		{ScopeRule{SubjectType: "unknown", SubjectID: "x"}, false},
	}
	for _, tc := range cases {
		if got := ruleApplies(tc.rule, subject); got != tc.want {
			t.Errorf("ruleApplies(%#v) = %v, want %v", tc.rule, got, tc.want)
		}
	}
}

func TestRetrievalContextAllows(t *testing.T) {
	context := RetrievalContext{
		OrgScope:         []string{"rd", "backend"},
		AllowedResources: []ResourceRef{{Kind: "model", ID: "glm"}},
		DeniedResources:  []ResourceRef{{Kind: "skill", ID: "dangerous"}},
	}
	cases := []struct {
		ref  ResourceRef
		want bool
	}{
		{ResourceRef{Kind: "team", ID: "rd"}, true},
		{ResourceRef{Kind: "model", ID: "glm"}, true},
		{ResourceRef{Kind: "skill", ID: "dangerous"}, false}, // explicit deny wins
		{ResourceRef{Kind: "team", ID: "sales"}, false},
	}
	for _, tc := range cases {
		if got := context.Allows(tc.ref); got != tc.want {
			t.Errorf("Allows(%#v) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

func TestBuildRetrievalContextDenyPrecedence(t *testing.T) {
	teams := map[string]TeamNode{
		"rd":      {ID: "rd", Parent: "", Path: "/rd"},
		"backend": {ID: "backend", Parent: "rd", Path: "/rd/backend"},
	}
	rules := []ScopeRule{
		{RuleKind: "management_scope", SubjectType: "user", SubjectID: "u1", ResourceKind: "team", ResourceID: "sales"},
		{RuleKind: "explicit_deny", SubjectType: "user", SubjectID: "u1", ResourceKind: "team", ResourceID: "backend"},
	}
	now := time.Now()
	context := BuildRetrievalContext("deployment-a", "u1", []string{"manager"}, []string{"rd"}, teams, rules, now)
	// Own subtree stays in org scope...
	if !contains(context.OrgScope, "backend") {
		t.Fatalf("OrgScope = %#v, want backend", context.OrgScope)
	}
	// ...but the explicit deny removes it from allowed decisions.
	if context.Allows(ResourceRef{Kind: "team", ID: "backend"}) {
		t.Fatal("explicit deny must override the own subtree")
	}
	// The management_scope grant adds sales.
	if !context.Allows(ResourceRef{Kind: "team", ID: "sales"}) {
		t.Fatal("management_scope grant must add sales")
	}
}

func TestCleanupRetentionGuards(t *testing.T) {
	t.Run("missing runtime database is an error", func(t *testing.T) {
		application := &App{}
		_, err := application.CleanupRetention(context.Background(), time.Now())
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("missing pool err = %v, want 'unavailable'", err)
		}
	})

	t.Run("non-positive batch size is an error", func(t *testing.T) {
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool(): %v", err)
		}
		t.Cleanup(pool.Close)
		application := &App{}
		application.SetRuntimeDatabase(pool)
		_, err = application.CleanupRetention(context.Background(), time.Now())
		if err == nil || !strings.Contains(err.Error(), "batch size") {
			t.Fatalf("bad batch size err = %v, want 'batch size'", err)
		}
	})

	t.Run("contended lock returns quietly", func(t *testing.T) {
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool(): %v", err)
		}
		t.Cleanup(pool.Close)
		application := &App{Config: config.Config{RetentionCleanupBatchSize: 100}}
		application.SetRuntimeDatabase(pool)
		pool.ExpectBegin()
		pool.ExpectQuery(`pg_try_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(false))
		pool.ExpectRollback()
		result, err := application.CleanupRetention(context.Background(), time.Now())
		if err != nil {
			t.Fatalf("CleanupRetention(): %v", err)
		}
		if result.LockAcquired {
			t.Fatalf("LockAcquired = true, want false")
		}
	})
}

func TestRunRetentionDisabled(t *testing.T) {
	// A non-positive interval returns immediately without touching the DB.
	application := &App{}
	application.RunRetention(context.Background())
}
