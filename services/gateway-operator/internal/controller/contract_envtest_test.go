package controller

import (
	"context"
	"path/filepath"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestOperatorContractAdmission(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	deployment := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "default"}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "local"}}
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Spec.DeploymentID = "renamed"
	if err := c.Update(ctx, deployment); !apierrors.IsInvalid(err) {
		t.Fatalf("deployment rename accepted: %v", err)
	}
	bad := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "another", Namespace: "default"}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "local"}}
	if err := c.Create(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatalf("second aggregation scope accepted: %v", err)
	}
	policy := &gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"}, Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "model"}, Quota: &gateway.Quota{Maximum: 100, IntervalSeconds: 60}}}
	if err := c.Create(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if policy.Spec.Quota.Unit != "requests" {
		t.Fatalf("default quota unit: %q", policy.Spec.Quota.Unit)
	}
	policy.Spec.Quota.IntervalSeconds = 59
	if err := c.Update(ctx, policy); !apierrors.IsInvalid(err) {
		t.Fatalf("unsupported native interval accepted: %v", err)
	}
	policy.Spec.Quota.IntervalSeconds = 60
	policy.Spec.Subject = &gateway.PolicySubject{Type: "user"}
	if err := c.Update(ctx, policy); !apierrors.IsInvalid(err) {
		t.Fatalf("missing subject id accepted: %v", err)
	}
	policy.Spec.Subject.ID = "alice"
	policy.Spec.Quota.Unit = "tokens"
	if err := c.Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	route := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "default"}, Spec: gateway.ModelRouteSpec{ModelID: "deepseek-chat", Protocol: "openai", ProviderType: "deepseek", Upstream: gateway.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "chat"}, Enabled: true}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatal(err)
	}
	if route.Spec.DeploymentRef != "deployment" || route.Spec.ProviderType != "deepseek" {
		t.Fatalf("route fields lost: %+v", route.Spec)
	}
}
