package workloadidentity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordedRegistry struct {
	current    []Registration
	registered []repository.ObservedModelWorkload
	retired    []string
	expected   []string
}

func (r *recordedRegistry) Registrations(context.Context, string, string) ([]Registration, error) {
	return r.current, nil
}
func (r *recordedRegistry) Register(_ context.Context, agent, expected string, w repository.ObservedModelWorkload, validFor time.Duration) (string, error) {
	if agent != "agent-1" || validFor != time.Minute {
		return "", errors.New("unexpected registry scope")
	}
	r.registered = append(r.registered, w)
	r.expected = append(r.expected, expected)
	return "new-binding", nil
}
func (r *recordedRegistry) Retire(_ context.Context, agent, key string) error {
	r.retired = append(r.retired, agent+":"+key)
	return nil
}

func employeeFixture() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "digitalemployee.zhiyuan.io/v1alpha1", "kind": "DigitalEmployee",
		"metadata": map[string]any{"name": "worker", "namespace": "employees", "uid": "employee-uid", "generation": int64(3), "resourceVersion": "10"},
		"spec":     map[string]any{"modelAccess": map[string]any{"agentId": "agent-1"}, "governance": map[string]any{"deploymentID": "deployment-1", "defaultAEPUser": "human-owner"}},
		"status":   map[string]any{"modelIdentity": map[string]any{"observedGeneration": int64(3), "agentId": "agent-1", "deploymentId": "deployment-1", "employeeUid": "employee-uid", "serviceAccountName": "worker-model", "serviceAccountUid": "sa-uid", "audience": "model-gateway"}},
	}}
}

func accountFixture() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker-model", Namespace: "employees", UID: "sa-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "digitalemployee.zhiyuan.io/v1alpha1", Kind: "DigitalEmployee", Name: "worker", UID: "employee-uid", Controller: ptr.To(true)}}}, AutomountServiceAccountToken: ptr.To(false)}
}

func observerFixture(t *testing.T, employee *unstructured.Unstructured, account *corev1.ServiceAccount) (*Observer, *recordedRegistry) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{}
	if employee != nil {
		objects = append(objects, employee)
	}
	if account != nil {
		objects = append(objects, account)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	registry := &recordedRegistry{}
	observer, err := New(reader, registry, "deployment-1", "employees", "model-gateway", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return observer, registry
}

func request() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "employees", Name: "worker"}}
}

func TestObserverRegistersOnlyVerifiedEmployeeAndOwnedAccount(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	result, err := o.Reconcile(t.Context(), request())
	if err != nil || result.RequeueAfter != 30*time.Second || len(r.registered) != 1 {
		t.Fatal("verified workload not renewed", result, err)
	}
	w := r.registered[0]
	if w.EmployeeName != "worker" || w.Identity.EmployeeUID != "employee-uid" || w.Identity.ServiceAccountUID != "sa-uid" || w.Identity.Audience != "model-gateway" || len(r.expected) != 1 || r.expected[0] != "" {
		t.Fatal("wrong workload observation", w)
	}
}

func TestObserverRejectsInvalidOrStaleEmployeeEvidence(t *testing.T) {
	cases := map[string]func(*unstructured.Unstructured, *corev1.ServiceAccount){
		"no explicit agent": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) {
			unstructured.RemoveNestedField(e.Object, "spec", "modelAccess")
		},
		"foreign deployment": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) {
			_ = unstructured.SetNestedField(e.Object, "foreign", "spec", "governance", "deploymentID")
		},
		"stale generation": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) { e.SetGeneration(4) },
		"forged agent": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) {
			_ = unstructured.SetNestedField(e.Object, "other-agent", "status", "modelIdentity", "agentId")
		},
		"forged employee UID": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) {
			_ = unstructured.SetNestedField(e.Object, "other-uid", "status", "modelIdentity", "employeeUid")
		},
		"wrong audience": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) {
			_ = unstructured.SetNestedField(e.Object, "kubernetes-api", "status", "modelIdentity", "audience")
		},
		"replaced account UID": func(_ *unstructured.Unstructured, s *corev1.ServiceAccount) { s.UID = "replacement" },
		"foreign owner UID": func(_ *unstructured.Unstructured, s *corev1.ServiceAccount) {
			s.OwnerReferences[0].UID = "other-employee"
		},
		"foreign owner type": func(_ *unstructured.Unstructured, s *corev1.ServiceAccount) { s.OwnerReferences[0].Kind = "Deployment" },
		"default API token": func(_ *unstructured.Unstructured, s *corev1.ServiceAccount) {
			s.AutomountServiceAccountToken = ptr.To(true)
		},
		"missing observation": func(e *unstructured.Unstructured, _ *corev1.ServiceAccount) {
			unstructured.RemoveNestedField(e.Object, "status", "modelIdentity")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e, s := employeeFixture(), accountFixture()
			mutate(e, s)
			o, r := observerFixture(t, e, s)
			r.current = []Registration{{AgentID: "agent-1", BindingKey: "old-binding"}}
			_, err := o.Reconcile(t.Context(), request())
			explicitRemoval := name == "no explicit agent" || name == "foreign deployment"
			if len(r.registered) != 0 {
				t.Fatal("untrusted identity was renewed", r)
			}
			if explicitRemoval {
				if err != nil || len(r.retired) != 1 || r.retired[0] != "agent-1:old-binding" {
					t.Fatal("removed binding not retired", err, r)
				}
			} else if err == nil || len(r.retired) != 0 {
				t.Fatal("unverified evidence should pause renewal without destroying history", err, r)
			}
		})
	}
}

func TestObserverDeletionRetiresOnlyKnownBinding(t *testing.T) {
	o, r := observerFixture(t, nil, nil)
	r.current = []Registration{{AgentID: "agent-1", BindingKey: "old-binding"}}
	_, err := o.Reconcile(t.Context(), request())
	if err != nil || len(r.registered) != 0 || len(r.retired) != 1 {
		t.Fatal("deleted identity was not retired", err)
	}
}

func TestObserverMissingAccountRetiresExactKnownBinding(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), nil)
	r.current = []Registration{{AgentID: "agent-1", BindingKey: "old-binding"}}
	if _, err := o.Reconcile(t.Context(), request()); err != nil || len(r.registered) != 0 || len(r.retired) != 1 || r.retired[0] != "agent-1:old-binding" {
		t.Fatal("account deletion did not retire the old identity", err, r)
	}
}

func TestObserverPassesRegistryCASOnAccountReplacement(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	r.current = []Registration{{AgentID: "agent-1", BindingKey: "old-binding"}}
	_, err := o.Reconcile(t.Context(), request())
	if err != nil || len(r.expected) != 1 || r.expected[0] != "old-binding" || len(r.retired) != 0 {
		t.Fatal("registry replacement fence bypassed", err, r)
	}
}

func TestObserverIgnoresOtherNamespace(t *testing.T) {
	o, r := observerFixture(t, employeeFixture(), accountFixture())
	_, err := o.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foreign", Name: "worker"}})
	if err != nil || len(r.registered) != 0 || len(r.retired) != 0 {
		t.Fatal("observer crossed namespace", err)
	}
}
