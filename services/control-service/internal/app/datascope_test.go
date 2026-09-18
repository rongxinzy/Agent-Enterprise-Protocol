package app

import (
	"testing"
	"time"
)

func testTeams() map[string]TeamNode {
	return map[string]TeamNode{
		"company":   {ID: "company", Parent: "", Path: "/company"},
		"rd":        {ID: "rd", Parent: "company", Path: "/company/rd"},
		"rd1":       {ID: "rd1", Parent: "rd", Path: "/company/rd/rd1"},
		"rd2":       {ID: "rd2", Parent: "rd", Path: "/company/rd/rd2"},
		"hr":        {ID: "hr", Parent: "company", Path: "/company/hr"},
		"hr-pay":    {ID: "hr-pay", Parent: "hr", Path: "/company/hr/hr-pay"},
		"stray-rd1": {ID: "stray-rd1", Parent: "", Path: "/"},
	}
}

func rule(id, kind, subjectType, subjectID, resourceKind, resourceID string) ScopeRule {
	return ScopeRule{ID: id, RuleKind: kind, SubjectType: subjectType, SubjectID: subjectID, ResourceKind: resourceKind, ResourceID: resourceID}
}

var evalNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func TestSubtreeIDsWalksParentLinks(t *testing.T) {
	subtree := subtreeIDs("rd", testTeams())
	want := []string{"rd", "rd1", "rd2"}
	if len(subtree) != len(want) {
		t.Fatalf("subtreeIDs(rd) = %v, want %v", subtree, want)
	}
	for index, id := range want {
		if subtree[index] != id {
			t.Fatalf("subtreeIDs(rd) = %v, want %v", subtree, want)
		}
	}
	if subtree := subtreeIDs("missing", testTeams()); len(subtree) != 1 || subtree[0] != "missing" {
		t.Fatalf("subtreeIDs(missing) = %v", subtree)
	}
	// A legacy row with the default path and no parent must stay standalone.
	if subtree := subtreeIDs("stray-rd1", testTeams()); len(subtree) != 1 {
		t.Fatalf("subtreeIDs(stray-rd1) = %v, want a single node", subtree)
	}
}

func TestBuildRetrievalContextOwnDepartmentOnly(t *testing.T) {
	context := BuildRetrievalContext("dep", "dev1", []string{"member"}, []string{"rd1"}, testTeams(), nil, evalNow)
	if len(context.OrgScope) != 1 || context.OrgScope[0] != "rd1" {
		t.Fatalf("OrgScope = %v, want [rd1]", context.OrgScope)
	}
	if context.CrossDepartmentReason != "" {
		t.Fatalf("CrossDepartmentReason = %q, want empty", context.CrossDepartmentReason)
	}
}

func TestBuildRetrievalContextMultiTeamUserUnionsSubtrees(t *testing.T) {
	context := BuildRetrievalContext("dep", "dev1", []string{"member"}, []string{"rd1", "hr"}, testTeams(), nil, evalNow)
	want := []string{"hr", "hr-pay", "rd1"}
	if len(context.OrgScope) != len(want) {
		t.Fatalf("OrgScope = %v, want %v", context.OrgScope, want)
	}
	for index, id := range want {
		if context.OrgScope[index] != id {
			t.Fatalf("OrgScope = %v, want %v", context.OrgScope, want)
		}
	}
	if len(context.OwnTeamIDs) != 2 {
		t.Fatalf("OwnTeamIDs = %v, want both own teams", context.OwnTeamIDs)
	}
}

func TestBuildRetrievalContextManagementScopeExpandsSubtree(t *testing.T) {
	rules := []ScopeRule{rule("r1", "management_scope", "role", "director", "team", "rd")}
	context := BuildRetrievalContext("dep", "mgr", []string{"director"}, []string{"rd1"}, testTeams(), rules, evalNow)
	want := []string{"rd", "rd1", "rd2"}
	if len(context.OrgScope) != len(want) {
		t.Fatalf("OrgScope = %v, want %v", context.OrgScope, want)
	}
	for index, id := range want {
		if context.OrgScope[index] != id {
			t.Fatalf("OrgScope = %v, want %v", context.OrgScope, want)
		}
	}
	if context.CrossDepartmentReason != "management_scope" {
		t.Fatalf("CrossDepartmentReason = %q", context.CrossDepartmentReason)
	}
	// The grant is role-bound: a member without the role gains nothing.
	other := BuildRetrievalContext("dep", "dev1", []string{"member"}, []string{"rd1"}, testTeams(), rules, evalNow)
	if len(other.OrgScope) != 1 {
		t.Fatalf("role-bound rule leaked: %v", other.OrgScope)
	}
}

func TestBuildRetrievalContextExceptionGrantAddsResource(t *testing.T) {
	rules := []ScopeRule{rule("r1", "exception_grant", "user", "dev1", "knowledge_base", "kb-hr")}
	context := BuildRetrievalContext("dep", "dev1", nil, []string{"rd1"}, testTeams(), rules, evalNow)
	if len(context.AllowedResources) != 1 || context.AllowedResources[0] != (ResourceRef{Kind: "knowledge_base", ID: "kb-hr"}) {
		t.Fatalf("AllowedResources = %v", context.AllowedResources)
	}
}

func TestBuildRetrievalContextSkipsExpiredAndFutureRules(t *testing.T) {
	past := evalNow.Add(-time.Hour)
	future := evalNow.Add(time.Hour)
	rules := []ScopeRule{
		{ID: "expired", RuleKind: "exception_grant", SubjectType: "user", SubjectID: "dev1", ResourceKind: "knowledge_base", ResourceID: "kb-x", ExpiresAt: &past},
		{ID: "scheduled", RuleKind: "exception_grant", SubjectType: "user", SubjectID: "dev1", ResourceKind: "knowledge_base", ResourceID: "kb-y", StartsAt: &future},
		{ID: "active", RuleKind: "exception_grant", SubjectType: "user", SubjectID: "dev1", ResourceKind: "knowledge_base", ResourceID: "kb-z"},
	}
	context := BuildRetrievalContext("dep", "dev1", nil, []string{"rd1"}, testTeams(), rules, evalNow)
	if len(context.AllowedResources) != 1 || context.AllowedResources[0] != (ResourceRef{Kind: "knowledge_base", ID: "kb-z"}) {
		t.Fatalf("AllowedResources = %v, want [kb-z]", context.AllowedResources)
	}
}

// The rule window is half-open: starts_at is inclusive, expires_at is
// exclusive, so a grant dies the instant it expires.
func TestBuildRetrievalContextWindowBoundaries(t *testing.T) {
	startsNow := evalNow
	expiresNow := evalNow
	rules := []ScopeRule{
		{ID: "starts-now", RuleKind: "exception_grant", SubjectType: "user", SubjectID: "dev1", ResourceKind: "knowledge_base", ResourceID: "kb-start", StartsAt: &startsNow},
		{ID: "expires-now", RuleKind: "exception_grant", SubjectType: "user", SubjectID: "dev1", ResourceKind: "knowledge_base", ResourceID: "kb-expire", ExpiresAt: &expiresNow},
	}
	context := BuildRetrievalContext("dep", "dev1", nil, []string{"rd1"}, testTeams(), rules, evalNow)
	if !containsResource(context.AllowedResources, ResourceRef{Kind: "knowledge_base", ID: "kb-start"}) {
		t.Fatalf("starts_at == now must be active, AllowedResources = %v", context.AllowedResources)
	}
	if containsResource(context.AllowedResources, ResourceRef{Kind: "knowledge_base", ID: "kb-expire"}) {
		t.Fatalf("expires_at == now must be expired, AllowedResources = %v", context.AllowedResources)
	}
}

func containsResource(values []ResourceRef, target ResourceRef) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// An expired explicit_deny stops denying: access recovers the moment the
// deny window closes.
func TestBuildRetrievalContextExpiredDenyRecovers(t *testing.T) {
	past := evalNow.Add(-time.Minute)
	deny := rule("d1", "explicit_deny", "user", "dev1", "team", "rd2")
	expiredDeny := rule("d2", "explicit_deny", "user", "dev1", "knowledge_base", "kb-hr")
	expiredDeny.ExpiresAt = &past
	grant := rule("g1", "exception_grant", "user", "dev1", "knowledge_base", "kb-hr")
	context := BuildRetrievalContext("dep", "dev1", nil, []string{"rd1"}, testTeams(), []ScopeRule{deny, expiredDeny, grant}, evalNow)
	if !context.Allows(ResourceRef{Kind: "knowledge_base", ID: "kb-hr"}) {
		t.Fatal("expired deny must stop blocking the granted resource")
	}
	if context.Allows(ResourceRef{Kind: "team", ID: "rd2"}) {
		t.Fatal("active deny on a team must still block it")
	}
}

func TestContextAllowsDenyWinsOverEveryAllowance(t *testing.T) {
	context := RetrievalContext{
		OrgScope:         []string{"rd1", "rd2"},
		AllowedResources: []ResourceRef{{Kind: "knowledge_base", ID: "kb-hr"}, {Kind: "classification", ID: "confidential"}},
		DeniedResources:  []ResourceRef{{Kind: "knowledge_base", ID: "kb-board"}, {Kind: "classification", ID: "薪酬保密"}, {Kind: "team", ID: "hr"}},
	}
	cases := []struct {
		name     string
		resource ResourceRef
		want     bool
	}{
		{"own department", ResourceRef{Kind: "team", ID: "rd1"}, true},
		{"granted knowledge base", ResourceRef{Kind: "knowledge_base", ID: "kb-hr"}, true},
		{"granted classification", ResourceRef{Kind: "classification", ID: "confidential"}, true},
		{"denied team", ResourceRef{Kind: "team", ID: "hr"}, false},
		{"denied classification", ResourceRef{Kind: "classification", ID: "薪酬保密"}, false},
		{"denied knowledge base", ResourceRef{Kind: "knowledge_base", ID: "kb-board"}, false},
		{"team outside org scope denied by default", ResourceRef{Kind: "team", ID: "hr-pay"}, false},
		{"empty resource denied by default", ResourceRef{}, false},
	}
	for _, test := range cases {
		if got := context.Allows(test.resource); got != test.want {
			t.Fatalf("%s: Allows(%+v) = %v, want %v", test.name, test.resource, got, test.want)
		}
	}
}

func TestBuildRetrievalContextExplicitDenyAppliesToTeamSubject(t *testing.T) {
	rules := []ScopeRule{rule("r1", "explicit_deny", "team", "rd1", "classification", "薪酬保密")}
	// dev1 belongs to rd1; the deny reaches them through the team subject.
	context := BuildRetrievalContext("dep", "dev1", nil, []string{"rd1"}, testTeams(), rules, evalNow)
	if !containsResource(context.DeniedResources, ResourceRef{Kind: "classification", ID: "薪酬保密"}) {
		t.Fatalf("DeniedResources = %v", context.DeniedResources)
	}
	if context.Allows(ResourceRef{Kind: "classification", ID: "薪酬保密"}) {
		t.Fatal("deny did not block the classification")
	}
}
