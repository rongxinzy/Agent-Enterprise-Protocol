// The kubebuilder gateway operator applies CR configuration to Higress.
package main

import (
	corev1 "k8s.io/api/core/v1"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1alpha1.AddToScheme(scheme))
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions())
	if err != nil {
		ctrl.Log.Error(err, "unable to start manager")
		os.Exit(1)
	}
	if err := wireManager(mgr); err != nil {
		ctrl.Log.Error(err, "unable to wire controllers")
		os.Exit(1)
	}
	ctrl.Log.Info("starting gateway operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "manager exited")
		os.Exit(1)
	}
}

// wireManager registers the controllers and health checks; split out so the
// wiring is testable without a live cluster.
func wireManager(mgr ctrl.Manager) error {
	runtime := &controller.Runtime{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(),
		CredentialNamespace: "aep-credentials", NativeSecretNamespace: "higress-system"}
	if err := (&controller.ModelRouteReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Runtime: runtime}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.ModelPolicyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Runtime: runtime}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.DeploymentConfigReconciler{Runtime: runtime}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return nil
}

// managerOptions builds the controller-runtime manager options: leader
// election on by default so only one replica applies resources, with a clean handoff on
// shutdown instead of holding the lease to expiry.
func managerOptions() ctrl.Options {
	return ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{"aep-system": {}},
			ByObject: map[client.Object]cache.ByObject{&corev1.Secret{}: {Namespaces: map[string]cache.Config{"aep-credentials": {}}}}},
		Metrics: metricsserver.Options{
			BindAddress: ":8092",
		},
		HealthProbeBindAddress:        ":8093",
		LeaderElection:                true,
		LeaderElectionID:              "gateway-operator.gateway.aep.zhiyuan.io",
		LeaderElectionNamespace:       "aep-system",
		LeaderElectionReleaseOnCancel: true,
	}
}
