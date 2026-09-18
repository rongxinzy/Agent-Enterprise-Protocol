package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// ScopeRule is the storage-agnostic view of one data_scope_rules row.
type ScopeRule struct {
	ID           string
	RuleKind     string // management_scope | exception_grant | explicit_deny
	SubjectType  string // user | role | team
	SubjectID    string
	ResourceKind string // 'team' or a deployment-defined opaque kind
	ResourceID   string
	StartsAt     *time.Time
	ExpiresAt    *time.Time
}

// ScopeSubject is the principal a rule set is evaluated for. Every value is
// resolved server side from stored bindings; request-provided identities are
// never trusted.
type ScopeSubject struct {
	UserID  string
	RoleIDs []string
	TeamIDs []string
}

// TeamNode is one node of the resolved department tree.
type TeamNode struct {
	ID     string
	Parent string
	Path   string
}

// RetrievalContext is the structured authorization contract handed to the
// policy enforcement point before any retrieval. Non-team resources are
// opaque (kind, id) references whose vocabulary is defined by the deployment,
// not by the protocol.
type RetrievalContext struct {
	PrincipalID           string        `json:"principalId"`
	DeploymentID          string        `json:"deploymentId"`
	OrgScope              []string      `json:"orgScope"`
	OwnTeamIDs            []string      `json:"ownTeamIds,omitempty"`
	RoleScope             []string      `json:"roleScope"`
	AllowedResources      []ResourceRef `json:"allowedResources,omitempty"`
	DeniedResources       []ResourceRef `json:"deniedResources,omitempty"`
	CrossDepartmentReason string        `json:"crossDepartmentReason,omitempty"`
}

// ResourceRef identifies the target a PEP decision is made for. Kind "team"
// is protocol-defined; every other kind is deployment-defined and opaque to
// the control plane.
type ResourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func ruleApplies(rule ScopeRule, subject ScopeSubject) bool {
	switch rule.SubjectType {
	case "user":
		return rule.SubjectID == subject.UserID
	case "role":
		for _, role := range subject.RoleIDs {
			if role == rule.SubjectID {
				return true
			}
		}
	case "team":
		for _, team := range subject.TeamIDs {
			if team == rule.SubjectID {
				return true
			}
		}
	}
	return false
}

func ruleInWindow(rule ScopeRule, now time.Time) bool {
	if rule.StartsAt != nil && now.Before(*rule.StartsAt) {
		return false
	}
	if rule.ExpiresAt != nil && !now.Before(*rule.ExpiresAt) {
		return false
	}
	return true
}

// subtreeIDs returns the team and every descendant by walking parent links.
// Parent traversal is authoritative; the materialized path column stays an
// index/debug aid so legacy rows with the default path still resolve.
func subtreeIDs(teamID string, teams map[string]TeamNode) []string {
	children := make(map[string][]string, len(teams))
	for id, node := range teams {
		children[node.Parent] = append(children[node.Parent], id)
	}
	var result []string
	frontier := []string{teamID}
	for len(frontier) > 0 {
		var next []string
		for _, id := range frontier {
			result = append(result, id)
			next = append(next, children[id]...)
		}
		frontier = next
	}
	return uniqueSorted(result)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// BuildRetrievalContext evaluates the rule layers with explicit deny
// precedence: explicit_deny > exception_grant > management_scope > the
// implicit own-department baseline (the own-team subtree). The implicit layer
// is the own-team subtree, which no rule can remove — only an explicit_deny
// on a resource can override it.
func BuildRetrievalContext(deploymentID, userID string, roles, ownTeams []string, teams map[string]TeamNode, rules []ScopeRule, now time.Time) RetrievalContext {
	orgScope := make([]string, 0, len(ownTeams))
	for _, teamID := range ownTeams {
		orgScope = append(orgScope, subtreeIDs(teamID, teams)...)
	}
	context := RetrievalContext{
		PrincipalID: userID, DeploymentID: deploymentID,
		OrgScope: uniqueSorted(orgScope), OwnTeamIDs: uniqueSorted(ownTeams), RoleScope: uniqueSorted(roles),
	}
	expansions := map[string]struct{}{}
	for _, rule := range rules {
		if rule.RuleKind != "management_scope" && rule.RuleKind != "exception_grant" {
			continue
		}
		if !ruleApplies(rule, ScopeSubject{UserID: userID, RoleIDs: roles, TeamIDs: ownTeams}) || !ruleInWindow(rule, now) {
			continue
		}
		if rule.ResourceKind == "team" {
			expansions[rule.ResourceID] = struct{}{}
			if context.CrossDepartmentReason == "" || !strings.Contains(context.CrossDepartmentReason, rule.RuleKind) {
				if context.CrossDepartmentReason == "" {
					context.CrossDepartmentReason = rule.RuleKind
				} else {
					context.CrossDepartmentReason += "," + rule.RuleKind
				}
			}
		} else {
			context.AllowedResources = append(context.AllowedResources, ResourceRef{Kind: rule.ResourceKind, ID: rule.ResourceID})
		}
	}
	for teamID := range expansions {
		context.OrgScope = uniqueSorted(append(context.OrgScope, subtreeIDs(teamID, teams)...))
	}
	for _, rule := range rules {
		if rule.RuleKind != "explicit_deny" || !ruleApplies(rule, ScopeSubject{UserID: userID, RoleIDs: roles, TeamIDs: ownTeams}) || !ruleInWindow(rule, now) {
			continue
		}
		context.DeniedResources = append(context.DeniedResources, ResourceRef{Kind: rule.ResourceKind, ID: rule.ResourceID})
	}
	context.AllowedResources = uniqueResourceRefs(context.AllowedResources)
	context.DeniedResources = uniqueResourceRefs(context.DeniedResources)
	return context
}

func uniqueResourceRefs(values []ResourceRef) []ResourceRef {
	seen := make(map[ResourceRef]struct{}, len(values))
	result := make([]ResourceRef, 0, len(values))
	for _, value := range values {
		if value.Kind == "" || value.ID == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// Allows is the default-deny PEP decision for one resource. Deny entries win
// over every allowance; absence of any matching allowance denies.
func (c RetrievalContext) Allows(resource ResourceRef) bool {
	for _, denied := range c.DeniedResources {
		if denied == resource {
			return false
		}
	}
	if resource.Kind == "team" && contains(c.OrgScope, resource.ID) {
		return true
	}
	for _, allowed := range c.AllowedResources {
		if allowed == resource {
			return true
		}
	}
	return false
}

// DataScopeContext resolves the retrieval context for one user from stored
// bindings, the department tree, and the effective rule set.
func (a *App) DataScopeContext(ctx context.Context, deploymentID, userID string) (RetrievalContext, error) {
	database := a.database()
	if database == nil {
		return RetrievalContext{}, errors.New("database is unavailable")
	}
	roles, err := a.UserRoleIDs(ctx, deploymentID, userID)
	if err != nil {
		return RetrievalContext{}, err
	}
	teamRows, err := database.Query(ctx, `SELECT t.id, COALESCE(t.parent_team_id,''), t.path FROM teams t
JOIN user_team_bindings utb ON utb.deployment_id=t.deployment_id AND utb.user_id=$2 AND utb.team_id=t.id
WHERE t.deployment_id=$1 AND t.enabled=true`, deploymentID, userID)
	if err != nil {
		return RetrievalContext{}, err
	}
	defer teamRows.Close()
	var ownTeams []string
	teamsByID := map[string]TeamNode{}
	for teamRows.Next() {
		node := TeamNode{}
		if err := teamRows.Scan(&node.ID, &node.Parent, &node.Path); err != nil {
			return RetrievalContext{}, err
		}
		ownTeams = append(ownTeams, node.ID)
		teamsByID[node.ID] = node
	}
	if err := teamRows.Err(); err != nil {
		return RetrievalContext{}, err
	}
	// The subtree computation needs every node on the path below the owned
	// teams, so load the full enabled tree for the deployment.
	treeRows, err := database.Query(ctx, `SELECT id, COALESCE(parent_team_id,''), path FROM teams WHERE deployment_id=$1 AND enabled=true`, deploymentID)
	if err != nil {
		return RetrievalContext{}, err
	}
	defer treeRows.Close()
	for treeRows.Next() {
		node := TeamNode{}
		if err := treeRows.Scan(&node.ID, &node.Parent, &node.Path); err != nil {
			return RetrievalContext{}, err
		}
		teamsByID[node.ID] = node
	}
	if err := treeRows.Err(); err != nil {
		return RetrievalContext{}, err
	}
	ruleRows, err := database.Query(ctx, `SELECT id, rule_kind, subject_type, subject_id, resource_kind, resource_id, starts_at, expires_at
FROM data_scope_rules
WHERE deployment_id=$1
  AND (starts_at IS NULL OR starts_at<=now())
  AND (expires_at IS NULL OR expires_at>now())
  AND ((subject_type='user' AND subject_id=$2)
    OR (subject_type='role' AND subject_id=ANY($3))
    OR (subject_type='team' AND subject_id=ANY($4)))`, deploymentID, userID, roles, ownTeams)
	if err != nil {
		return RetrievalContext{}, err
	}
	defer ruleRows.Close()
	var rules []ScopeRule
	for ruleRows.Next() {
		rule := ScopeRule{}
		if err := ruleRows.Scan(&rule.ID, &rule.RuleKind, &rule.SubjectType, &rule.SubjectID, &rule.ResourceKind, &rule.ResourceID, &rule.StartsAt, &rule.ExpiresAt); err != nil {
			return RetrievalContext{}, err
		}
		rules = append(rules, rule)
	}
	if err := ruleRows.Err(); err != nil {
		return RetrievalContext{}, err
	}
	// Own team nodes may be absent from teamsByID when a binding points at a
	// disabled team; the subtree walk keeps them as bare scope entries.
	return BuildRetrievalContext(deploymentID, userID, roles, ownTeams, teamsByID, rules, time.Now()), nil
}
