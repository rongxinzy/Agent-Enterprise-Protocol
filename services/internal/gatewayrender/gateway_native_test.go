package gatewayrender

import (
	"strings"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

func nativeFixture() (Input, gatewaypolicy.Publication, NativeGatewayConfig) {
	d := Input{Deployment: "tenant-a", Routes: []Route{{ModelID: "a", Enabled: true, Protocol: "openai-compatible"}, {ModelID: "b", Enabled: true, Protocol: "anthropic"}, {ModelID: "disabled", Enabled: false}}}
	p := gatewaypolicy.Publication{Items: []gatewaypolicy.Limit{{ID: "rule-a", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "second", Enabled: true}}, {ID: "rule-b", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "tokens", ScopeType: "team", ScopeID: nativeText("team-b"), ModelID: nativeText("a"), Maximum: 10, Interval: "minute", Enabled: true}}}}
	p.Revision = gatewaypolicy.Revision(p.Items)
	return d, p, NativeGatewayConfig{Enabled: true, RedisService: "redis.higress-system.svc.cluster.local", RedisPort: 6379, RedisDatabase: 1, RedisUsername: "default"}
}
func nativeText(value string) *string { return &value }

func TestRenderNativeLimitsAndQuota(t *testing.T) {
	d, p, cfg := nativeFixture()
	resources, err := RenderGatewayLimits(d, p, cfg, "disposable-redis-fixture")
	if err != nil || len(resources) != 2 || !strings.Contains(resources[0].Body, requestLimitURL) || !strings.Contains(resources[1].Body, tokenLimitURL) || !strings.Contains(resources[1].Body, "token_per_minute") || !strings.Contains(resources[1].Body, gatewaypolicy.Header(p.Items[1].Configuration)) || strings.Contains(resources[1].Body, "regexp:") {
		t.Fatal(resources, err)
	}
	if resources[0].APIPath == resources[1].APIPath {
		t.Fatal("rules share a plugin")
	}
	d2 := d
	d2.Deployment = "tenant-b"
	other, err := RenderGatewayLimits(d2, p, cfg, "")
	if err != nil || other[0].APIPath == resources[0].APIPath {
		t.Fatal("tenant shares counters")
	}
	p.Items[0].Configuration.Enabled = false
	p.Revision = gatewaypolicy.Revision(p.Items)
	disabled, err := RenderGatewayLimits(d, p, cfg, "")
	if err != nil || !strings.Contains(disabled[0].Body, "matchRules: []") {
		t.Fatal("tombstone active")
	}
	if _, err := RenderGatewayLimits(d, p, NativeGatewayConfig{}, ""); err == nil {
		t.Fatal("missing Redis accepted")
	}
	p.Revision = "forged"
	if _, err := RenderGatewayLimits(d, p, cfg, ""); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	p.Items[0].Configuration.Kind = "bad"
	p.Revision = gatewaypolicy.Revision(p.Items)
	if _, err := RenderGatewayLimits(d, p, cfg, ""); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	quota, err := RenderGatewayQuota(d, cfg, "disposable-redis-fixture", "disposable-admin-fixture")
	if err != nil || len(quota) != 3 || !strings.Contains(quota[0].Body, "/v1/chat/completions/quota") || !strings.Contains(quota[1].Body, quotaURL) || !strings.Contains(quota[2].Body, quotaAuthURL) || !strings.Contains(quota[2].Body, "Bearer disposable-admin-fixture") {
		t.Fatal(quota, err)
	}
	if _, err := RenderGatewayQuota(d, cfg, "", ""); err == nil {
		t.Fatal("unprotected quota route")
	}
	d.Routes = nil
	p.Items = nil
	p.Revision = gatewaypolicy.Revision(p.Items)
	if _, err := RenderGatewayLimits(d, p, cfg, ""); err != nil {
		t.Fatal(err)
	}
}
