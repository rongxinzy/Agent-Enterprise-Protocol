// Package gatewaypolicy defines configuration for native Higress plugins.
// It never counts requests/tokens or enforces limits itself.
package gatewaypolicy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Configuration struct {
	Kind            string  `json:"kind"`
	ScopeType       string  `json:"scopeType"`
	ScopeID         *string `json:"scopeId"`
	ModelID         *string `json:"modelId"`
	Maximum         int64   `json:"maximum"`
	Interval        string  `json:"interval"`
	Enabled         bool    `json:"enabled"`
	ExpectedVersion int64   `json:"expectedVersion"`
}

type Limit struct {
	ID            string        `json:"id"`
	Version       int64         `json:"version"`
	Configuration Configuration `json:"configuration"`
	UpdatedAt     time.Time     `json:"updatedAt"`
}

type Publication struct {
	Revision    string    `json:"revision"`
	PublishedAt time.Time `json:"publishedAt"`
	Items       []Limit   `json:"items"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }
func (c Configuration) Valid() bool {
	if (c.Kind != "requests" && c.Kind != "tokens") || c.Maximum < 1 || c.Maximum > 1e12 || c.ExpectedVersion < 0 {
		return false
	}
	if c.Interval != "second" && c.Interval != "minute" && c.Interval != "hour" && c.Interval != "day" {
		return false
	}
	if c.ModelID != nil && (strings.TrimSpace(*c.ModelID) == "" || len(*c.ModelID) > 256) {
		return false
	}
	if c.ScopeType == "global" {
		return c.ScopeID == nil
	}
	if c.ScopeType != "model" && c.ScopeType != "user" && c.ScopeType != "team" && c.ScopeType != "role" {
		return false
	}
	return c.ScopeID != nil && strings.TrimSpace(*c.ScopeID) != "" && len(*c.ScopeID) <= 256 && (c.ScopeType != "model" || c.ModelID == nil || *c.ModelID == *c.ScopeID)
}

func Revision(items []Limit) string {
	items = append([]Limit(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	data, _ := json.Marshal(items)
	sum := sha256.Sum256(data)
	return "limits-" + hex.EncodeToString(sum[:])
}

func key(kind, id, model string) string {
	modelPart := "all"
	if model != "" {
		modelPart = "m." + base64.RawURLEncoding.EncodeToString([]byte(model))
	}
	return kind + "." + base64.RawURLEncoding.EncodeToString([]byte(id)) + "." + modelPart
}

func Pattern(c Configuration) string {
	model := ""
	if c.ModelID != nil {
		model = *c.ModelID
	}
	id := ""
	if c.ScopeID != nil {
		id = *c.ScopeID
	}
	if c.ScopeType == "model" {
		return "regexp:.*" + regexp.QuoteMeta("|"+key("global", "", id)+"|") + ".*"
	}
	return "regexp:.*" + regexp.QuoteMeta("|"+key(c.ScopeType, id, model)+"|") + ".*"
}

// Keys supplies exact trusted membership keys. One native plugin per rule
// means overlapping rules are independent even on first-match artifacts.
func Keys(model, user string, teams, roles []string) (string, error) {
	parts := []string{key("global", "", ""), key("global", "", model), key("user", user, ""), key("user", user, model)}
	for kind, ids := range map[string][]string{"team": teams, "role": roles} {
		for _, id := range ids {
			parts = append(parts, key(kind, id, ""), key(kind, id, model))
		}
	}
	sort.Strings(parts)
	value := "|" + strings.Join(parts, "|") + "|"
	if len(value) > 32<<10 {
		return "", errors.New("too many gateway policy memberships")
	}
	return value, nil
}
