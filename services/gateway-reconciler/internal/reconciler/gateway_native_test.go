package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

// The render-side coverage for native limits/quota lives in the shared
// gatewayrender package tests; this file keeps the reconciler's sync and
// acknowledgment loop coverage.

func nativeSyncFixture() (DesiredState, gatewaypolicy.Publication, gatewayrender.NativeGatewayConfig) {
	d := DesiredState{DeploymentID: "tenant-a", Routes: []gatewayrender.Route{{ModelID: "a", Enabled: true, Protocol: "openai-compatible"}, {ModelID: "b", Enabled: true, Protocol: "anthropic"}, {ModelID: "disabled", Enabled: false}}}
	team, model := "team-b", "a"
	p := gatewaypolicy.Publication{Items: []gatewaypolicy.Limit{{ID: "rule-a", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "requests", ScopeType: "global", Maximum: 1, Interval: "second", Enabled: true}}, {ID: "rule-b", Version: 1, Configuration: gatewaypolicy.Configuration{Kind: "tokens", ScopeType: "team", ScopeID: &team, ModelID: &model, Maximum: 10, Interval: "minute", Enabled: true}}}}
	p.Revision = gatewaypolicy.Revision(p.Items)
	return d, p, gatewayrender.NativeGatewayConfig{Enabled: true, RedisService: "redis.higress-system.svc.cluster.local", RedisPort: 6379, RedisDatabase: 1, RedisUsername: "default"}
}

type nativeApplier struct {
	fail  bool
	count int
}

func (a *nativeApplier) Apply(context.Context, DesiredState, []gatewayrender.RenderedResource) error {
	return nil
}
func (a *nativeApplier) ApplyGatewayNative(_ context.Context, resources []gatewayrender.RenderedResource) error {
	a.count += len(resources)
	if a.fail {
		return errors.New("apply unavailable")
	}
	return nil
}

func TestNativeReconciliationAndAcknowledgment(t *testing.T) {
	for _, failure := range []string{"", "read", "malformed", "empty", "hash", "apply", "ack", "secret", "missing-secret-resolver", "quota", "no-applier"} {
		t.Run(failure, func(t *testing.T) {
			d, p, cfg := nativeSyncFixture()
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
				r.config.NativeGateway.RedisPasswordRef = &gatewayrender.SecretReference{Name: "redis", Key: "password"}
			}
			if failure == "secret" {
				r.config.CredentialFetcher = func(context.Context, gatewayrender.SecretReference) (string, error) { return "", errors.New("missing") }
			}
			if failure == "quota" {
				r.config.NativeGateway.QuotaAdminCredentialRef = &gatewayrender.SecretReference{Name: "quota", Key: "key"}
				r.config.CredentialFetcher = func(context.Context, gatewayrender.SecretReference) (string, error) { return "local-fixture", nil }
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
