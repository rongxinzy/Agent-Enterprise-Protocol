// The kubebuilder gateway operator applies CR configuration to Higress.
package main

import (
	corev1 "k8s.io/api/core/v1"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gatewayv1alpha1 "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/access"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/controller"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/dataplane"
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
	options, err := accessOptions(os.Getenv)
	if err != nil {
		return err
	}
	runtime := &controller.Runtime{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(),
		CredentialNamespace: "aep-credentials", NativeSecretNamespace: "higress-system"}
	if name := strings.TrimSpace(os.Getenv("AEP_GATEWAY_OBSERVATION_DEPLOYMENT")); name != "" {
		config := rest.CopyConfig(mgr.GetConfig())
		config.Timeout = 5 * time.Second
		kube, err := kubernetes.NewForConfig(config)
		if err != nil {
			return err
		}
		runtime.DataPlane = &dataplane.Fleet{Reader: mgr.GetAPIReader(), Deployment: client.ObjectKey{Namespace: "higress-system", Name: name}, Dump: dataplane.PodDump(config, kube)}
	}
	if err := (&controller.ModelRouteReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Runtime: runtime}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.ModelPolicyReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Runtime: runtime}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.DeploymentConfigReconciler{Runtime: runtime}).SetupWithManager(mgr); err != nil {
		return err
	}
	if options != nil {
		if err := (&access.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Options: *options}).SetupWithManager(mgr); err != nil {
			return err
		}
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
	byObject := map[client.Object]cache.ByObject{&corev1.Secret{}: {Namespaces: map[string]cache.Config{"aep-credentials": {}}}}
	// Native routes live with Higress; do not expand all other informer scopes.
	if os.Getenv("AEP_GATEWAY_ACCESS_INGRESS_HOST") != "" {
		routes := &unstructured.Unstructured{}
		routes.SetAPIVersion("networking.istio.io/v1alpha3")
		routes.SetKind("EnvoyFilter")
		byObject[routes] = cache.ByObject{Namespaces: map[string]cache.Config{"higress-system": {}}}
	}
	return ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{"aep-system": {}},
			ByObject: byObject},
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
