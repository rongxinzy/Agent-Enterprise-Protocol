// Package workloadidentity observes trusted Kubernetes lifecycle evidence. It
// does not grant models, issue bearer tokens or accept runtime-supplied claims.
package workloadidentity

import (
	"context"
	"errors"
	"time"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var employeeGVK = schema.GroupVersionKind{Group: "digitalemployee.zhiyuan.io", Version: "v1alpha1", Kind: "DigitalEmployee"}
var errUnverified = errors.New("workload lifecycle evidence is not verified")

type Registration struct{ AgentID, BindingKey string }

type Registry interface {
	Registrations(context.Context, string, string) ([]Registration, error)
	Register(context.Context, string, string, repository.ObservedModelWorkload, time.Duration) (string, error)
	Retire(context.Context, string, string) error
}

// Reader must be Manager.GetAPIReader(), never the informer cache. Cached
// events schedule work only; live reads establish the evidence being renewed.
type Observer struct {
	reader                          client.Reader
	registry                        Registry
	deployment, namespace, audience string
	freshness                       time.Duration
}

func New(reader client.Reader, registry Registry, deployment, namespace, audience string, freshness time.Duration) (*Observer, error) {
	if reader == nil || registry == nil || deployment == "" || namespace == "" || audience == "" || freshness < 2*time.Second || freshness > time.Hour || freshness%time.Second != 0 {
		return nil, errors.New("invalid workload observer configuration")
	}
	return &Observer{reader: reader, registry: registry, deployment: deployment, namespace: namespace, audience: audience, freshness: freshness}, nil
}

func (o *Observer) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	if request.Namespace != o.namespace {
		return ctrl.Result{}, nil
	}
	current, err := o.registry.Registrations(ctx, request.Namespace, request.Name)
	if err != nil {
		return ctrl.Result{}, errors.New("workload registry observation unavailable")
	}
	employee := &unstructured.Unstructured{}
	employee.SetGroupVersionKind(employeeGVK)
	if err := o.reader.Get(ctx, request.NamespacedName, employee); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, o.retire(ctx, current)
		}
		return ctrl.Result{}, errors.New("employee lifecycle observation unavailable")
	}
	if !employee.GetDeletionTimestamp().IsZero() || text(employee.Object, "spec", "modelAccess", "agentId") == "" || text(employee.Object, "spec", "governance", "deploymentID") != o.deployment {
		return ctrl.Result{}, o.retire(ctx, current)
	}
	agent, observation, err := o.evidence(ctx, employee)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, o.retire(ctx, current)
		}
		// Missing/stale status and API outages must not renew, but are not
		// permanent deletion evidence. Retiring a still-existing binding would
		// prevent recovery because its immutable history cannot be resurrected.
		return ctrl.Result{}, errUnverified
	}
	// A second direct read fences changes during the SA lookup. Kubernetes and
	// DB are not atomic: native watches plus the bounded observation lifetime
	// handle a later change; no stale event is treated as positive evidence.
	latest := &unstructured.Unstructured{}
	latest.SetGroupVersionKind(employeeGVK)
	if err := o.reader.Get(ctx, request.NamespacedName, latest); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, o.retire(ctx, current)
		}
		return ctrl.Result{}, errors.New("employee lifecycle confirmation unavailable")
	}
	if latest.GetUID() != employee.GetUID() || latest.GetResourceVersion() != employee.GetResourceVersion() || !latest.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, errors.New("employee changed during workload observation")
	}
	expected := ""
	for _, registration := range current {
		if registration.AgentID == agent {
			if expected != "" {
				return ctrl.Result{}, errors.New("ambiguous workload registry identity")
			}
			expected = registration.BindingKey
		} else if err := o.registry.Retire(ctx, registration.AgentID, registration.BindingKey); err != nil {
			return ctrl.Result{}, errors.New("previous workload retirement failed")
		}
	}
	if _, err := o.registry.Register(ctx, agent, expected, observation, o.freshness); err != nil {
		return ctrl.Result{}, errors.New("verified workload registration failed")
	}
	return ctrl.Result{RequeueAfter: o.freshness / 2}, nil
}

func (o *Observer) retire(ctx context.Context, registrations []Registration) error {
	for _, registration := range registrations {
		if err := o.registry.Retire(ctx, registration.AgentID, registration.BindingKey); err != nil {
			return errors.New("workload retirement failed")
		}
	}
	return nil
}

func text(object map[string]any, path ...string) string {
	value, _, _ := unstructured.NestedString(object, path...)
	return value
}

func (o *Observer) evidence(ctx context.Context, employee *unstructured.Unstructured) (string, repository.ObservedModelWorkload, error) {
	w := repository.ObservedModelWorkload{EmployeeName: employee.GetName()}
	agent := text(employee.Object, "spec", "modelAccess", "agentId")
	status, found, err := unstructured.NestedMap(employee.Object, "status", "modelIdentity")
	generation, _, _ := unstructured.NestedInt64(status, "observedGeneration")
	if err != nil || !found || agent == "" || employee.GetUID() == "" || employee.GetGeneration() < 1 || !employee.GetDeletionTimestamp().IsZero() ||
		text(employee.Object, "spec", "governance", "deploymentID") != o.deployment || generation != employee.GetGeneration() ||
		text(status, "deploymentId") != o.deployment || text(status, "agentId") != agent || text(status, "employeeUid") != string(employee.GetUID()) || text(status, "audience") != o.audience {
		return "", w, errUnverified
	}
	sa := &corev1.ServiceAccount{}
	if err := o.reader.Get(ctx, types.NamespacedName{Namespace: employee.GetNamespace(), Name: text(status, "serviceAccountName")}, sa); err != nil {
		return "", w, err
	}
	owner := metav1.GetControllerOf(sa)
	if sa.UID == "" || text(status, "serviceAccountUid") != string(sa.UID) || !sa.DeletionTimestamp.IsZero() ||
		sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken || owner == nil ||
		owner.UID != employee.GetUID() || owner.Name != employee.GetName() || owner.Kind != employeeGVK.Kind || owner.APIVersion != employeeGVK.GroupVersion().String() {
		return "", w, errUnverified
	}
	w.Identity.Namespace = employee.GetNamespace()
	w.Identity.EmployeeUID = employee.GetUID()
	w.Identity.ServiceAccountName = sa.Name
	w.Identity.ServiceAccountUID = sa.UID
	w.Identity.Audience = o.audience
	w.PodUIDs, err = o.livePods(ctx, employee, sa)
	if err != nil {
		return "", w, errUnverified
	}
	return agent, w, nil
}
