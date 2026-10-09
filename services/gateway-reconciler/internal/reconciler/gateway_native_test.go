package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

func nativeFixture() (DesiredState, gatewaypolicy.Publication, NativeGatewayConfig) {
	d := DesiredState{DeploymentID: "tenant-a", Routes: []Route{{ModelID: "a", Enabled: true, Protocol: "openai-compatible"}, {ModelID: "b", Enabled: true, Protocol: "anthropic"}, {ModelID: "disabled", Enabled: false}}}
	p := gatewaypolicy.Publication{Items: []gatewaypolicy.Limit{{ID: "rule-a", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "second", Enabled: true}}, {ID: "rule-b", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "tokens", ScopeType: "team", ScopeID: nativeText("team-b"), ModelID: nativeText("a"), Maximum: 10, Interval: "minute", Enabled: true}}}}
	p.Revision = gatewaypolicy.Revision(p.Items)
	return d, p, NativeGatewayConfig{Enabled: true, RedisService: "redis.higress-system.svc.cluster.local", RedisPort: 6379, RedisDatabase: 1, RedisUsername: "default"}
}
func nativeText(value string) *string { return &value }

func TestRenderNativeLimitsAndQuota(t *testing.T) {
	d, p, cfg := nativeFixture()
	resources, err := RenderGatewayLimits(d, p, cfg, "disposable-redis-fixture")
	if err != nil || len(resources) != 2 || !strings.Contains(resources[0].Body, requestLimitURL) || !strings.Contains(resources[1].Body, tokenLimitURL) || !strings.Contains(resources[1].Body, "token_per_minute") || !strings.Contains(resources[1].Body, "x-aep-limit-keys") {
		t.Fatal(resources, err)
	}
	if resources[0].APIPath == resources[1].APIPath {
		t.Fatal("rules share a plugin")
	}
	d2 := d
	d2.DeploymentID = "tenant-b"
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

type nativeApplier struct {
	fail  bool
	count int
}

func (a *nativeApplier) Apply(context.Context, DesiredState, []RenderedResource) error { return nil }
func (a *nativeApplier) ApplyGatewayNative(_ context.Context, resources []RenderedResource) error {
	a.count += len(resources)
	if a.fail {
		return errors.New("apply unavailable")
	}
	return nil
}

func TestNativeReconciliationAndAcknowledgment(t *testing.T) {
	for _, failure := range []string{"", "read", "malformed", "empty", "hash", "apply", "ack", "secret", "missing-secret-resolver", "quota", "no-applier"} {
		t.Run(failure, func(t *testing.T) {
			d, p, cfg := nativeFixture()
			applier := &nativeApplier{fail: failure == "apply"}
			ackState := ""
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-AEP-Data-Plane-Token") != "service" || r.Header.Get("X-AEP-Deployment-ID") != "tenant-a" {
					t.Error("native sync identity")
				}
				if r.Method == http.MethodGet {
					if failure == "read" {
						w.WriteHeader(503)
						return
					}
					if failure == "malformed" {
						_, _ = fmt.Fprint(w, "bad")
						return
					}
					if failure == "empty" {
						p.Revision = ""
					}
					if failure == "hash" {
						p.Revision = "forged"
					}
					_ = json.NewEncoder(w).Encode(p)
					return
				}
				var ack map[string]string
				_ = json.NewDecoder(r.Body).Decode(&ack)
				ackState = ack["state"]
				if failure == "ack" {
					w.WriteHeader(409)
				}
			}))
			defer control.Close()
			r := &Reconciler{config: Config{ControlURL: control.URL, Token: "service", HTTPClient: control.Client(), Applier: applier, NativeGateway: cfg}}
			if failure == "secret" || failure == "missing-secret-resolver" {
				r.config.NativeGateway.RedisPasswordRef = &SecretReference{Name: "redis", Key: "password"}
			}
			if failure == "secret" {
				r.config.CredentialFetcher = func(context.Context, SecretReference) (string, error) { return "", errors.New("missing") }
			}
			if failure == "quota" {
				r.config.NativeGateway.QuotaAdminCredentialRef = &SecretReference{Name: "quota", Key: "key"}
				r.config.CredentialFetcher = func(context.Context, SecretReference) (string, error) { return "local-fixture", nil }
			}
			if failure == "no-applier" {
				r.config.Applier = nil
			}
			err := r.syncGatewayLimits(context.Background(), d)
			if failure == "" || failure == "quota" {
				if err != nil || ackState != "applied" || applier.count < 2 {
					t.Fatal(err, ackState, applier.count)
				}
			} else if failure == "empty" {
				if err != nil || applier.count != 0 {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure accepted", failure)
			}
		})
	}
}
