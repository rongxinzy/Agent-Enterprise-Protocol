package controller

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

var controllerTestScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(controllerTestScheme))
	utilruntime.Must(gatewayv1alpha1.AddToScheme(controllerTestScheme))
}

func routeFixture(name string) *gatewayv1alpha1.ModelRoute {
	return &gatewayv1alpha1.ModelRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec: gatewayv1alpha1.ModelRouteSpec{
			ModelID:  "deepseek-chat",
			Protocol: "openai",
			Upstream: gatewayv1alpha1.ModelRouteUpstream{Endpoint: "https://provider.example/v1", Model: "deepseek-chat"},
			Enabled:  true,
		},
	}
}

func TestModelRouteReconcileReadFailurePropagates(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(controllerTestScheme).Build()
	broken := interceptor.NewClient(base, interceptor.Funcs{Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
		return apierrors.NewServiceUnavailable("apiserver down")
	}})
	reconciler := &ModelRouteReconciler{Client: broken, Scheme: controllerTestScheme}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "route-a", Namespace: "default"}}); err == nil {
		t.Fatal("read failure must propagate")
	}
}

func TestModelRouteReconcileStatusWriteFailurePropagates(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(routeFixture("route-a")).WithStatusSubresource(&gatewayv1alpha1.ModelRoute{}).Build()
	broken := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
		return apierrors.NewConflict(gatewayv1alpha1.GroupVersion.WithResource("modelroutes").GroupResource(), "route-a", errors.New("outdated"))
	}})
	reconciler := &ModelRouteReconciler{Client: broken, Scheme: controllerTestScheme}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "route-a", Namespace: "default"}}); err == nil {
		t.Fatal("status write failure must propagate")
	}
}

func TestModelPolicyReconcileTargetReadFailurePropagates(t *testing.T) {
	policy := &gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-a", Namespace: "default", Generation: 1},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-a"},
		},
	}
	base := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(policy).WithStatusSubresource(&gatewayv1alpha1.ModelPolicy{}).Build()
	var calls int
	broken := interceptor.NewClient(base, interceptor.Funcs{Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
		calls++
		if _, isRoute := obj.(*gatewayv1alpha1.ModelRoute); isRoute {
			return apierrors.NewServiceUnavailable("apiserver down")
		}
		return base.Get(context.Background(), key, obj)
	}})
	reconciler := &ModelPolicyReconciler{Client: broken, Scheme: controllerTestScheme}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "policy-a", Namespace: "default"}}); err == nil {
		t.Fatal("target read failure must propagate")
	}
	if calls == 0 {
		t.Fatal("target read was never attempted")
	}
}

func TestModelPolicyReconcileStatusWriteFailurePropagates(t *testing.T) {
	policy := &gatewayv1alpha1.ModelPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-a", Namespace: "default", Generation: 1},
		Spec: gatewayv1alpha1.ModelPolicySpec{
			TargetRef: gatewayv1alpha1.PolicyTargetReference{Kind: "ModelRoute", Name: "route-a"},
		},
	}
	base := fake.NewClientBuilder().WithScheme(controllerTestScheme).WithObjects(policy, routeFixture("route-a")).WithStatusSubresource(&gatewayv1alpha1.ModelPolicy{}).Build()
	broken := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
		return apierrors.NewConflict(gatewayv1alpha1.GroupVersion.WithResource("modelpolicies").GroupResource(), "policy-a", errors.New("outdated"))
	}})
	reconciler := &ModelPolicyReconciler{Client: broken, Scheme: controllerTestScheme}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "policy-a", Namespace: "default"}}); err == nil {
		t.Fatal("status write failure must propagate")
	}
}
