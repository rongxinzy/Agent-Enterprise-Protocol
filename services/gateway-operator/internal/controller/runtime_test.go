package controller

import (
	"context"
	"strings"
	"testing"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUnsafeRenderedObjectsRejected(t *testing.T) {
	for _, body := range []string{"{", `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"unexpected","namespace":"higress-system"}}`, `{"apiVersion":"networking.k8s.io/v1","kind":"Ingress","metadata":{"name":"unexpected","namespace":"default"}}`} {
		if _, err := object(gatewayrender.RenderedResource{Body: body}); err == nil {
			t.Fatal("unsafe rendered object accepted")
		}
	}
	if err := (&Runtime{}).prune(context.Background(), "", nil); err == nil {
		t.Fatal("cleanup without owner UID accepted")
	}
}

func TestSnapshotRejectsUnusableInputs(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	baseline := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "aep-system", UID: "deployment-uid"}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "test"}}
	route := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "aep-system", UID: "route-uid"}, Spec: gateway.ModelRouteSpec{ModelID: "alpha", Protocol: "openai", Upstream: gateway.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "alpha"}, Enabled: true}}
	run := func(objects []client.Object, want string) {
		t.Helper()
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		rt := &Runtime{Client: c, Reader: c, CredentialNamespace: "aep-credentials", NativeSecretNamespace: "higress-system"}
		_, err := rt.snapshot(ctx, client.ObjectKeyFromObject(baseline))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q, got %v", want, err)
		}
	}
	t.Run("missing deployment", func(t *testing.T) { run(nil, "deployment configuration unavailable") })
	t.Run("terminating deployment", func(t *testing.T) {
		d := baseline.DeepCopy()
		d.DeletionTimestamp = ptrTime()
		d.Finalizers = []string{finalizer}
		run([]client.Object{d}, "terminating")
	})
	t.Run("duplicate model", func(t *testing.T) {
		other := route.DeepCopy()
		other.Name = "duplicate"
		run([]client.Object{baseline, route, other}, "duplicate model")
	})
	t.Run("invalid endpoint", func(t *testing.T) {
		bad := route.DeepCopy()
		bad.Spec.Upstream.Endpoint = "https://secret:password@provider.example"
		run([]client.Object{baseline, bad}, "route spec invalid")
	})
	t.Run("missing provider secret", func(t *testing.T) {
		bad := route.DeepCopy()
		bad.Spec.CredentialRef = &gateway.CredentialReference{Name: "missing", Key: "key"}
		run([]client.Object{baseline, bad}, "credential unavailable")
	})
	t.Run("missing key", func(t *testing.T) {
		bad := route.DeepCopy()
		bad.Spec.CredentialRef = &gateway.CredentialReference{Name: "provider", Key: "key"}
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "provider", Namespace: "aep-credentials"}, Data: map[string][]byte{"other": []byte("must-not-leak")}}
		run([]client.Object{baseline, bad, s}, "credential unavailable")
	})
	t.Run("native disabled", func(t *testing.T) {
		p := &gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "aep-system"}, Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "alpha"}, Quota: &gateway.Quota{Maximum: 10, IntervalSeconds: 60}}}
		run([]client.Object{baseline, route, p}, "enabled Redis-backed")
	})
	t.Run("unsupported protocol", func(t *testing.T) {
		bad := route.DeepCopy()
		bad.Spec.Protocol = "invalid"
		run([]client.Object{baseline, bad}, "rendering failed")
	})
	t.Run("native Redis invalid", func(t *testing.T) {
		d := baseline.DeepCopy()
		d.Spec.NativeGateway = &gateway.NativeGatewaySpec{Enabled: true}
		run([]client.Object{d, route}, "native limit rendering failed")
	})
	t.Run("native credential missing", func(t *testing.T) {
		d := baseline.DeepCopy()
		d.Spec.NativeGateway = &gateway.NativeGatewaySpec{Enabled: true, RedisService: "redis", RedisPort: 6379, RedisPasswordRef: &gateway.CredentialReference{Name: "missing", Key: "password"}}
		run([]client.Object{d, route}, "credential unavailable")
	})
	t.Run("quota credential missing", func(t *testing.T) {
		d := baseline.DeepCopy()
		d.Spec.NativeGateway = &gateway.NativeGatewaySpec{Enabled: true, RedisService: "redis", RedisPort: 6379, QuotaAdminCredentialRef: &gateway.CredentialReference{Name: "missing", Key: "admin"}}
		run([]client.Object{d, route}, "credential unavailable")
	})
}

func ptrTime() *metav1.Time { now := metav1.Now(); return &now }
