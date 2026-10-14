package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

// envtest binary discovery: KUBEBUILDER_ASSETS wins, then the repo-local
// services/gateway-operator/bin/envtest tree (see README step in AGENTS.md).
// CI downloads the pinned envtest-v1.37.0 asset into KUBEBUILDER_ASSETS.
func envtestAssets(t *testing.T) string {
	t.Helper()
	if assets := os.Getenv("KUBEBUILDER_ASSETS"); assets != "" {
		return assets
	}
	candidate := filepath.Join("..", "..", "bin", "envtest", "controller-tools", "envtest")
	if _, err := os.Stat(candidate); err == nil {
		absolute, err := filepath.Abs(candidate)
		if err == nil {
			return absolute
		}
	}
	t.Skip("envtest binaries not found (set KUBEBUILDER_ASSETS or run scripts/envtest-setup)")
	return ""
}

// The suite boots one local etcd+apiserver pair with the generated CRDs and
// runs the manager in-process; every subtest shares it.
func TestControllersWithEnvtest(t *testing.T) {
	log.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1alpha1.AddToScheme(scheme))

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		BinaryAssetsDirectory: envtestAssets(t),
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	defer func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("envtest stop: %v", err)
		}
	}()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		// The suite may run more than once in a single test binary
		// (go test -count=N); controller-runtime's process-global controller
		// name registry would otherwise reject the repeated registrations.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	// The shadow render rides every accepted ModelRoute reconcile against the
	// real cache, exercising the deployment-scope list + render wiring.
	if err := (&ModelRouteReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ShadowDeployment: "envtest-shadow"}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err := (&ModelPolicyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager exited: %v", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("manager cache never synced")
	}
	k8sClient := mgr.GetClient()

	namespace := "default"

	validRoute := func(name string) *gatewayv1alpha1.ModelRoute {
		return &gatewayv1alpha1.ModelRoute{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: gatewayv1alpha1.ModelRouteSpec{
				ModelID:  "deepseek-chat",
				Protocol: "openai",
				Upstream: gatewayv1alpha1.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "deepseek-chat"},
				Enabled:  true,
			},
		}
	}

	waitAccepted := func(kind, name string, wantStatus metav1.ConditionStatus, wantReason string, wantGeneration int64) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var conditions []metav1.Condition
			var generation int64
			switch kind {
			case "ModelRoute":
				var route gatewayv1alpha1.ModelRoute
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &route); err == nil {
					conditions = route.Status.Conditions
					generation = route.Status.ObservedGeneration
				}
			case "ModelPolicy":
				var policy gatewayv1alpha1.ModelPolicy
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &policy); err == nil {
					conditions = policy.Status.Conditions
					generation = policy.Status.ObservedGeneration
				}
			}
			for _, condition := range conditions {
				if condition.Type == gatewayv1alpha1.ConditionAccepted && condition.Status == wantStatus && condition.Reason == wantReason && condition.ObservedGeneration == wantGeneration && generation == wantGeneration {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s %s never reached Accepted=%s/%s at generation %d (conditions=%v, observedGeneration=%d)", kind, name, wantStatus, wantReason, wantGeneration, conditions, generation)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Run("valid route is accepted and tracks its generation", func(t *testing.T) {
		route := validRoute("valid-route")
		route.Spec.TenantScope = &gatewayv1alpha1.TenantScope{Teams: []string{"qa"}, Roles: []string{"ops"}}
		route.Spec.CredentialRef = &gatewayv1alpha1.CredentialReference{Name: "provider-secrets", Key: "deepseek"}
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelRoute", "valid-route", metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, 1)

		// The status write must not clobber the spec (status subresource
		// isolation): re-read and compare the spec fields.
		var stored gatewayv1alpha1.ModelRoute
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "valid-route", Namespace: namespace}, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Spec.TenantScope == nil || len(stored.Spec.TenantScope.Teams) != 1 || stored.Spec.TenantScope.Teams[0] != "qa" {
			t.Fatalf("status write clobbered spec.tenantScope: %#v", stored.Spec.TenantScope)
		}
		if stored.Spec.CredentialRef == nil || stored.Spec.CredentialRef.Key != "deepseek" {
			t.Fatalf("status write clobbered spec.credentialRef: %#v", stored.Spec.CredentialRef)
		}
		if stored.Spec.Upstream.Model != "deepseek-chat" || !stored.Spec.Enabled {
			t.Fatalf("status write clobbered spec: %#v", stored.Spec)
		}

		// An update bumps the generation and is re-reconciled.
		stored.Spec.Upstream.Model = "deepseek-v4"
		if err := k8sClient.Update(ctx, &stored); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelRoute", "valid-route", metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, 2)
	})

	t.Run("invalid endpoint is rejected with a semantic reason", func(t *testing.T) {
		// The CRD schema pattern accepts any http(s)-prefixed string; the
		// credential-in-URL rejection lives in the controller's shallow
		// validation (the schema cannot express it).
		route := validRoute("invalid-route")
		route.Spec.Upstream.Endpoint = "http://user:pass@provider.example/v1"
		if err := k8sClient.Create(ctx, route); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelRoute", "invalid-route", metav1.ConditionFalse, gatewayv1alpha1.ReasonInvalidSpec, 1)
	})

	t.Run("policy on an existing target is accepted", func(t *testing.T) {
		policy := &gatewayv1alpha1.ModelPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "policy-ok", Namespace: namespace},
			Spec: gatewayv1alpha1.ModelPolicySpec{
				TargetRef:  gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "valid-route"},
				RateLimits: []gatewayv1alpha1.RateLimit{{Maximum: 100, IntervalSeconds: 60}},
				Quota:      &gatewayv1alpha1.Quota{Maximum: 1000, IntervalSeconds: 3600},
			},
		}
		if err := k8sClient.Create(ctx, policy); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelPolicy", "policy-ok", metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, 1)
	})

	t.Run("policy is revalidated when its target appears", func(t *testing.T) {
		// The ModelRoute watch must requeue policies that reference the route,
		// so a target created after the policy flips TargetNotFound to Accepted.
		policy := &gatewayv1alpha1.ModelPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "late-policy", Namespace: namespace},
			Spec: gatewayv1alpha1.ModelPolicySpec{
				TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "late-route"},
			},
		}
		if err := k8sClient.Create(ctx, policy); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelPolicy", "late-policy", metav1.ConditionFalse, gatewayv1alpha1.ReasonTargetNotFound, 1)

		if err := k8sClient.Create(ctx, validRoute("late-route")); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelPolicy", "late-policy", metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, 1)
	})

	t.Run("policy is revalidated when its target is deleted", func(t *testing.T) {
		if err := k8sClient.Create(ctx, validRoute("vanishing-route")); err != nil {
			t.Fatal(err)
		}
		policy := &gatewayv1alpha1.ModelPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "vanishing-policy", Namespace: namespace},
			Spec: gatewayv1alpha1.ModelPolicySpec{
				TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "vanishing-route"},
			},
		}
		if err := k8sClient.Create(ctx, policy); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelPolicy", "vanishing-policy", metav1.ConditionTrue, gatewayv1alpha1.ReasonAccepted, 1)

		if err := k8sClient.Delete(ctx, validRoute("vanishing-route")); err != nil {
			t.Fatal(err)
		}
		waitAccepted("ModelPolicy", "vanishing-policy", metav1.ConditionFalse, gatewayv1alpha1.ReasonTargetNotFound, 1)
	})

	t.Run("reconcile of a missing object is a no-op", func(t *testing.T) {
		routeReconciler := &ModelRouteReconciler{Client: k8sClient, Scheme: scheme}
		result, err := routeReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "deleted-route", Namespace: namespace}})
		if err != nil || result.RequeueAfter != 0 {
			t.Fatalf("missing route reconcile = %v, %#v", err, result)
		}
		policyReconciler := &ModelPolicyReconciler{Client: k8sClient, Scheme: scheme}
		policyResult, err := policyReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "deleted-policy", Namespace: namespace}})
		if err != nil || policyResult.RequeueAfter != 0 {
			t.Fatalf("missing policy reconcile = %v, %#v", err, policyResult)
		}
	})

	t.Run("reconcile of an unchanged object writes nothing", func(t *testing.T) {
		// The route is already Accepted at its current generation; a second
		// reconcile must take the no-change path and leave the status intact.
		routeReconciler := &ModelRouteReconciler{Client: k8sClient, Scheme: scheme}
		if _, err := routeReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "valid-route", Namespace: namespace}}); err != nil {
			t.Fatal(err)
		}
		var route gatewayv1alpha1.ModelRoute
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "valid-route", Namespace: namespace}, &route); err != nil {
			t.Fatal(err)
		}
		if len(route.Status.Conditions) != 1 || route.Status.Conditions[0].Reason != gatewayv1alpha1.ReasonAccepted {
			t.Fatalf("conditions after idempotent reconcile = %#v", route.Status.Conditions)
		}
	})
}
