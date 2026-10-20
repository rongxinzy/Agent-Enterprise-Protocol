package workloadidentity

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type lifecycleEvent struct {
	workload repository.ObservedModelWorkload
	retired  string
}

type watchedRegistry struct {
	sync.Mutex
	current []Registration
	events  chan lifecycleEvent
}

func (r *watchedRegistry) Registrations(context.Context, string, string) ([]Registration, error) {
	r.Lock()
	defer r.Unlock()
	return append([]Registration(nil), r.current...), nil
}

func (r *watchedRegistry) Register(_ context.Context, agent, _ string, w repository.ObservedModelWorkload, _ time.Duration) (string, error) {
	r.Lock()
	defer r.Unlock()
	r.current = []Registration{{AgentID: agent, BindingKey: "registered-binding"}}
	r.events <- lifecycleEvent{workload: w}
	return "registered-binding", nil
}

func (r *watchedRegistry) Retire(_ context.Context, _ string, key string) error {
	r.Lock()
	defer r.Unlock()
	r.current = nil
	r.events <- lifecycleEvent{retired: key}
	return nil
}

func nextLifecycleEvent(t *testing.T, events <-chan lifecycleEvent, match func(lifecycleEvent) bool) lifecycleEvent {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-deadline.C:
			t.Fatal("native controller did not observe lifecycle change")
		}
	}
}

// The employee schema belongs to the governance repository. Schema-neutral
// storage here tests native watches, status isolation and real UID/RV reads,
// without claiming to validate governance admission or run its Operator.
func TestObserverControllerWithAPIServer(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("set KUBEBUILDER_ASSETS for lifecycle controller API tests")
	}
	crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "digitalemployees.digitalemployee.zhiyuan.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: employeeGVK.Group, Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "digitalemployees", Singular: "digitalemployee", Kind: employeeGVK.Kind, ListKind: "DigitalEmployeeList"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: employeeGVK.Version, Served: true, Storage: true,
				Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}},
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"spec": {Type: "object", XPreserveUnknownFields: ptr.To(true)}, "status": {Type: "object", XPreserveUnknownFields: ptr.To(true)},
				}}}}}}}
	environment := &envtest.Environment{BinaryAssetsDirectory: assets, CRDs: []*apiextensionsv1.CustomResourceDefinition{crd}}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0", Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{"employees": {}}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &watchedRegistry{events: make(chan lifecycleEvent, 64)}
	if err := Setup(mgr, r, "deployment-1", "employees", "model-gateway", time.Minute); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("controller did not drain")
		}
	})
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "employees"}}); err != nil {
		t.Fatal(err)
	}
	e := employeeFixture()
	e.SetUID("")
	e.SetResourceVersion("")
	e.SetGeneration(0)
	delete(e.Object, "status")
	if err := c.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	sa := accountFixture()
	sa.UID = ""
	sa.OwnerReferences[0].UID = e.GetUID()
	if err := c.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	e.Object["status"] = map[string]any{"modelIdentity": map[string]any{
		"observedGeneration": e.GetGeneration(), "agentId": "agent-1", "deploymentId": "deployment-1",
		"employeeUid": string(e.GetUID()), "serviceAccountName": sa.Name, "serviceAccountUid": string(sa.UID), "audience": "model-gateway",
	}}
	if err := c.Status().Update(ctx, e); err != nil {
		t.Fatal(err)
	}
	first := nextLifecycleEvent(t, r.events, func(e lifecycleEvent) bool { return e.workload.EmployeeName != "" })
	if first.workload.Identity.EmployeeUID != e.GetUID() || first.workload.Identity.ServiceAccountUID != sa.UID {
		t.Fatal("controller did not register real live UIDs")
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(e), e); err != nil || text(e.Object, "spec", "modelAccess", "agentId") != "agent-1" {
		t.Fatal("status update clobbered employee spec", err)
	}
	verifyControllerPodWatches(t, c, e, sa, r.events)
	// SA deletion is a separate watch; no employee write is needed to retire.
	if err := c.Delete(ctx, sa); err != nil {
		t.Fatal(err)
	}
	nextLifecycleEvent(t, r.events, func(e lifecycleEvent) bool { return e.retired == "registered-binding" })
}
