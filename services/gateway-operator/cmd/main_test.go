package main

import (
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
)

func TestManagerOptions(t *testing.T) {
	options := managerOptions()
	if !options.LeaderElection {
		t.Fatal("leader election must be on by default")
	}
	if options.LeaderElectionID != "gateway-operator.gateway.aep.zhiyuan.io" {
		t.Fatalf("LeaderElectionID = %q", options.LeaderElectionID)
	}
	if options.LeaderElectionNamespace != "aep-system" {
		t.Fatalf("LeaderElectionNamespace = %q", options.LeaderElectionNamespace)
	}
	if !options.LeaderElectionReleaseOnCancel {
		t.Fatal("the lease must be released on a clean shutdown")
	}
	if options.Metrics.BindAddress != ":8092" || options.HealthProbeBindAddress != ":8093" {
		t.Fatalf("unexpected bind addresses: %#v", options)
	}
	if options.Scheme == nil {
		t.Fatal("scheme is nil")
	}
}

func TestWireManagerRegistersControllersAndHealth(t *testing.T) {
	// The manager is constructed without ever dialing the API server. Bind
	// addresses are disabled so a repeated run (go test -count=N) does not
	// collide on ports the previous manager never released; controller name
	// validation is process-global, so it is skipped for the same reason.
	// The static RESTMapper keeps field-index and watch registration offline:
	// the default lazy mapper would resolve kinds through discovery.
	options := managerOptions()
	options.Metrics.BindAddress = "0"
	options.HealthProbeBindAddress = "0"
	options.Controller.SkipNameValidation = ptr.To(true)
	options.MapperProvider = func(_ *rest.Config, _ *http.Client) (meta.RESTMapper, error) {
		mapper := meta.NewDefaultRESTMapper(nil)
		mapper.Add(gatewayv1alpha1.GroupVersion.WithKind("ModelRoute"), meta.RESTScopeNamespace)
		mapper.Add(gatewayv1alpha1.GroupVersion.WithKind("ModelPolicy"), meta.RESTScopeNamespace)
		mapper.Add(gatewayv1alpha1.GroupVersion.WithKind("DeploymentConfig"), meta.RESTScopeNamespace)
		mapper.Add(corev1.SchemeGroupVersion.WithKind("Secret"), meta.RESTScopeNamespace)
		return mapper, nil
	}
	mgr, err := ctrl.NewManager(&rest.Config{Host: "localhost:59090"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := wireManager(mgr); err != nil {
		t.Fatal(err)
	}
}
