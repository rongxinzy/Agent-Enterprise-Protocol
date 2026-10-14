package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type creationRaceReader struct {
	client.Reader
	writer  client.Client
	foreign *unstructured.Unstructured
}

func (r *creationRaceReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := r.Reader.Get(ctx, key, obj, opts...)
	if apierrors.IsNotFound(err) && key == client.ObjectKeyFromObject(r.foreign) {
		if createErr := r.writer.Create(ctx, r.foreign); createErr != nil {
			return createErr
		}
	}
	return err
}

// Real API server tests exercise watch delivery, SSA, status isolation and
// finalizers. Higress CRDs here are schema fixtures, not a running data plane.
func TestAppliedControllersWithEnvtest(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases"), "testdata"}, BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	}()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gateway.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", Controller: config.Controller{SkipNameValidation: ptr.To(true)}})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), CredentialNamespace: "aep-credentials", NativeSecretNamespace: "higress-system"}
	aggregate := &DeploymentConfigReconciler{Runtime: rt}
	if err := aggregate.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err := (&ModelRouteReconciler{Client: mgr.GetClient(), Scheme: scheme, Runtime: rt}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err := (&ModelPolicyReconciler{Client: mgr.GetClient(), Scheme: scheme, Runtime: rt}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache sync")
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	updateSpec := func(obj client.Object) error {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return err
		}
		patch, err := json.Marshal(map[string]any{"spec": data["spec"]})
		if err != nil {
			return err
		}
		return c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch))
	}
	for _, name := range []string{"aep-system", "higress-system", "aep-credentials"} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("reconciliation did not converge")
	}
	deployment := &gateway.DeploymentConfig{ObjectMeta: metav1.ObjectMeta{Name: "deployment", Namespace: "aep-system"}, Spec: gateway.DeploymentConfigSpec{DeploymentID: "test"}}
	defer func() {
		if t.Failed() {
			var latest gateway.DeploymentConfig
			if c.Get(ctx, client.ObjectKeyFromObject(deployment), &latest) == nil {
				t.Logf("aggregate convergence status: %+v", latest.Status)
			}
		}
	}()
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	waitDeployment := func(applied bool) {
		t.Helper()
		wait(func() bool {
			if c.Get(ctx, client.ObjectKeyFromObject(deployment), deployment) != nil {
				return false
			}
			condition := meta.FindStatusCondition(deployment.Status.Conditions, gateway.ConditionApplied)
			return condition != nil && (condition.Status == metav1.ConditionTrue) == applied
		})
	}
	waitDeployment(true)
	if meta.IsStatusConditionTrue(deployment.Status.Conditions, gateway.ConditionReady) {
		t.Fatal("SSA claimed data-plane Ready")
	}
	route := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "aep-system"}, Spec: gateway.ModelRouteSpec{ModelID: "alpha", Protocol: "openai", ProviderType: "deepseek", Upstream: gateway.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "chat"}, Enabled: true}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatal(err)
	}
	waitRoute := func(applied bool) {
		t.Helper()
		lastStatus := ""
		wait(func() bool {
			if c.Get(ctx, client.ObjectKeyFromObject(route), route) != nil {
				return false
			}
			condition := meta.FindStatusCondition(route.Status.Conditions, gateway.ConditionApplied)
			status, _ := json.Marshal(route.Status)
			if string(status) != lastStatus {
				lastStatus = string(status)
				t.Logf("route convergence status: %s", lastStatus)
			}
			return condition != nil && condition.ObservedGeneration == route.Generation && (condition.Status == metav1.ConditionTrue) == applied
		})
	}
	waitRoute(true)
	if len(route.Status.AppliedResources) != 2 || meta.IsStatusConditionTrue(route.Status.Conditions, gateway.ConditionReady) {
		t.Fatal("route boundary/readiness")
	}
	get := func(kind, name string) *unstructured.Unstructured {
		t.Helper()
		obj := &unstructured.Unstructured{}
		for _, gvk := range renderedGVKs {
			if gvk.Kind == kind {
				obj.SetGroupVersionKind(gvk)
			}
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: "higress-system", Name: name}, obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	ingressName := gatewayrender.OpenAIResourceName("test", "alpha")
	ingress := get("Ingress", ingressName)
	if ingress.GetLabels()[ownerLabel] != string(route.UID) || len(ingress.GetOwnerReferences()) != 0 {
		t.Fatal("invalid cross-namespace ownership")
	}
	pluginName := "aep-ai-proxy-" + gatewayrender.ResourceSuffix("test")
	plugin := get("WasmPlugin", pluginName)
	if plugin.GetLabels()[ownerLabel] != string(deployment.UID) {
		t.Fatal("shared plugin owned by route")
	}
	provider, _, _ := unstructured.NestedSlice(plugin.Object, "spec", "matchRules")
	if provider[0].(map[string]any)["config"].(map[string]any)["provider"].(map[string]any)["type"] != "deepseek" {
		t.Fatal("provider adapter lost")
	}

	// Route update preserves spec and removes resources belonging to the prior name.
	oldName := ingressName
	route.Spec.ModelID = "beta"
	route.Spec.Upstream.Model = "new-chat"
	if err := updateSpec(route); err != nil {
		t.Fatal(err)
	}
	waitRoute(true)
	ingressName = gatewayrender.OpenAIResourceName("test", "beta")
	get("Ingress", ingressName)
	old := &unstructured.Unstructured{}
	old.SetGroupVersionKind(schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"})
	if err := c.Get(ctx, client.ObjectKey{Namespace: "higress-system", Name: oldName}, old); !apierrors.IsNotFound(err) {
		t.Fatalf("obsolete ingress survived: %v", err)
	}
	if route.Spec.Upstream.Model != "new-chat" {
		t.Fatal("status clobbered spec")
	}

	// A declared but missing provider credential withdraws the route; it is
	// never silently rendered as an anonymous provider.
	route.Spec.CredentialRef = &gateway.CredentialReference{Name: "provider", Key: "key"}
	if err := updateSpec(route); err != nil {
		t.Fatal(err)
	}
	waitDeployment(false)
	waitRoute(false)
	if err := c.Get(ctx, client.ObjectKey{Namespace: "higress-system", Name: ingressName}, old); !apierrors.IsNotFound(err) {
		t.Fatalf("credential failure left route open: %v", err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "provider", Namespace: "aep-credentials"}, Data: map[string][]byte{"key": []byte("envtest-mock-credential")}}
	if err := c.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	// The metadata-only watch triggers credential refresh without caching values.
	waitDeployment(true)
	waitRoute(true)
	previousHash := deployment.Status.AppliedSnapshot
	secret.Data["key"] = []byte("envtest-rotated-mock")
	if err := c.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		_ = c.Get(ctx, client.ObjectKeyFromObject(deployment), deployment)
		return deployment.Status.AppliedSnapshot != previousHash
	})
	if strings.Contains(deployment.Status.Conditions[0].Message, "envtest-") {
		t.Fatal("credential leaked into status")
	}

	// A policy mutation updates the aggregate without writing route-owned objects.
	nativeSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "aep-native-gateway", Namespace: "higress-system"}, Data: map[string][]byte{"password": []byte("envtest-mock-redis"), "admin": []byte("envtest-mock-admin")}}
	if err := c.Create(ctx, nativeSecret); err != nil {
		t.Fatal(err)
	}
	deployment.Spec.NativeGateway = &gateway.NativeGatewaySpec{Enabled: true, RedisService: "redis.higress-system.svc.cluster.local", RedisPort: 6379,
		RedisPasswordRef: &gateway.CredentialReference{Name: "aep-native-gateway", Key: "password"}, QuotaAdminCredentialRef: &gateway.CredentialReference{Name: "aep-native-gateway", Key: "admin"}}
	if err := updateSpec(deployment); err != nil {
		t.Fatal(err)
	}
	policy := &gateway.ModelPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "aep-system"}, Spec: gateway.ModelPolicySpec{TargetRef: gateway.PolicyTargetReference{Kind: "ModelRoute", Name: "alpha"}, Subject: &gateway.PolicySubject{Type: "user", ID: "alice"}, Quota: &gateway.Quota{Unit: "tokens", Maximum: 100, IntervalSeconds: 60}}}
	if err := c.Create(ctx, policy); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		_ = c.Get(ctx, client.ObjectKeyFromObject(policy), policy)
		return meta.IsStatusConditionTrue(policy.Status.Conditions, gateway.ConditionApplied)
	})
	if err := c.Delete(ctx, policy); err != nil {
		t.Fatal(err)
	}
	waitDeployment(true)
	wait(func() bool {
		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(schema.GroupVersionKind{Group: "extensions.higress.io", Version: "v1alpha1", Kind: "WasmPluginList"})
		if c.List(ctx, &list, client.InNamespace("higress-system")) != nil {
			return false
		}
		return len(list.Items) == 4
	})

	// Cleanup ignores inventory supplied through status and never deletes a
	// foreign UID. Removing the final route empties deployment match rules.
	foreign := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": "foreign", "namespace": "higress-system", "labels": map[string]any{ownerLabel: "another-owner"}}, "spec": map[string]any{"defaultBackend": map[string]any{"service": map[string]any{"name": "test", "port": map[string]any{"number": int64(80)}}}}}}
	if err := c.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(foreign.Object)
	if _, err := rt.apply(ctx, route, []gatewayrender.RenderedResource{{Kind: gatewayrender.ResourceOpenAIIngress, Body: string(body)}}, "collision"); err == nil || !strings.Contains(err.Error(), "ownership conflict") {
		t.Fatalf("foreign resource takeover: %v", err)
	}
	// A foreign writer creates an unlabelled object between our not-found
	// read and write. SSA alone could claim its previously unowned fields.
	raceObject := foreign.DeepCopy()
	raceObject.SetName("foreign-race")
	raceObject.SetUID("")
	raceObject.SetResourceVersion("")
	raceObject.SetManagedFields(nil)
	raceObject.SetLabels(nil)
	raceBody, err := json.Marshal(raceObject.Object)
	if err != nil {
		t.Fatal(err)
	}
	raceRuntime := &Runtime{Client: c, Reader: &creationRaceReader{Reader: c, writer: c, foreign: raceObject}}
	if _, err := raceRuntime.apply(ctx, route, []gatewayrender.RenderedResource{{Kind: gatewayrender.ResourceOpenAIIngress, Body: string(raceBody)}}, "race"); err == nil {
		t.Fatal("not-found race adopted a foreign resource")
	}
	if raced := get("Ingress", "foreign-race"); raced.GetUID() != raceObject.GetUID() || raced.GetLabels()[ownerLabel] != "" || raced.GetAnnotations()[snapshotAnnotation] != "" {
		t.Fatal("foreign resource was modified during creation race")
	}
	if err := c.Delete(ctx, route); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		return apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(route), &gateway.ModelRoute{}))
	})
	wait(func() bool {
		p := &unstructured.Unstructured{}
		p.SetGroupVersionKind(schema.GroupVersionKind{Group: "extensions.higress.io", Version: "v1alpha1", Kind: "WasmPlugin"})
		if c.Get(ctx, client.ObjectKey{Namespace: "higress-system", Name: pluginName}, p) != nil {
			return false
		}
		rules, _, _ := unstructured.NestedSlice(p.Object, "spec", "matchRules")
		return len(rules) == 0
	})
	get("Ingress", "foreign")
	get("Ingress", "foreign-race")
	if err := c.Delete(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		return apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(deployment), &gateway.DeploymentConfig{}))
	})
	get("Ingress", "foreign")
	wait(func() bool {
		p := &unstructured.Unstructured{}
		p.SetGroupVersionKind(schema.GroupVersionKind{Group: "extensions.higress.io", Version: "v1alpha1", Kind: "WasmPlugin"})
		return apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: "higress-system", Name: pluginName}, p))
	})
}
