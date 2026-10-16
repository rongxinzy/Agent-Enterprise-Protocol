package dataplane

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errFleet = errors.New("gateway replicas are unavailable or changing")

// Evidence is safe metadata only. It is bound to the caller's snapshot and
// refreshed on every successful full-fleet observation, independently of
// condition LastTransitionTime.
type Evidence struct {
	ObservedAt    time.Time
	DeploymentUID types.UID
	PodUIDs       []types.UID
}

type Fleet struct {
	Reader     client.Reader
	Deployment client.ObjectKey
	Dump       func(context.Context, core.Pod) ([]byte, error)
}

type roster struct {
	deployment apps.Deployment
	pods       []core.Pod
	service    core.Service
	endpoints  []discovery.EndpointSlice
}

func readyPod(p *core.Pod) bool {
	if !p.DeletionTimestamp.IsZero() || p.Status.Phase != core.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == core.PodReady {
			return c.Status == core.ConditionTrue
		}
	}
	return false
}

func ownedBy(refs []metav1.OwnerReference, kind string, uid types.UID) bool {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller && r.Kind == kind && r.UID == uid {
			return true
		}
	}
	return false
}

func (f *Fleet) roster(ctx context.Context) (roster, error) {
	var r roster
	if f.Reader.Get(ctx, f.Deployment, &r.deployment) != nil {
		return r, errFleet
	}
	d := &r.deployment
	if d.UID == "" || !d.DeletionTimestamp.IsZero() || d.Spec.Replicas == nil || *d.Spec.Replicas < 1 || *d.Spec.Replicas > 64 || d.Spec.Selector == nil ||
		d.Status.ObservedGeneration != d.Generation || d.Status.Replicas != *d.Spec.Replicas || d.Status.UpdatedReplicas != *d.Spec.Replicas ||
		d.Status.ReadyReplicas != *d.Spec.Replicas || d.Status.AvailableReplicas != *d.Spec.Replicas {
		return r, errFleet
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return r, errFleet
	}
	var sets apps.ReplicaSetList
	var pods core.PodList
	if f.Reader.List(ctx, &sets, client.InNamespace(d.Namespace), client.MatchingLabelsSelector{Selector: selector}) != nil ||
		f.Reader.List(ctx, &pods, client.InNamespace(d.Namespace), client.MatchingLabelsSelector{Selector: selector}) != nil {
		return r, errFleet
	}
	owners := map[types.UID]bool{}
	for _, s := range sets.Items {
		if ownedBy(s.OwnerReferences, "Deployment", d.UID) {
			owners[s.UID] = true
		}
	}
	for _, p := range pods.Items {
		owned := false
		for uid := range owners {
			if ownedBy(p.OwnerReferences, "ReplicaSet", uid) {
				owned = true
				break
			}
		}
		// Even a terminating gateway may still serve a draining connection; do not
		// issue complete-fleet evidence while any owned pod is outside the stable roster.
		if owned {
			if !readyPod(&p) || p.UID == "" {
				return r, errFleet
			}
			r.pods = append(r.pods, p)
		}
	}
	if len(r.pods) != int(*d.Spec.Replicas) {
		return r, errFleet
	}
	// The configured Service is the traffic boundary. Deployment labels alone
	// cannot prove that all pods selected by the Service have been covered.
	// Deployment and Service deliberately share one configured ObjectKey.
	// A differently named Service requires a reviewed deployment overlay;
	// never guess it from labels or silently reduce the observed traffic set.
	if f.Reader.Get(ctx, f.Deployment, &r.service) != nil || r.service.UID == "" || !r.service.DeletionTimestamp.IsZero() || len(r.service.Spec.Selector) == 0 {
		return r, errFleet
	}
	portFound := false
	for _, p := range r.service.Spec.Ports {
		if p.Port == 80 && p.TargetPort.IntValue() == 80 {
			portFound = true
		}
	}
	if !portFound {
		return r, errFleet
	}
	var slices discovery.EndpointSliceList
	if f.Reader.List(ctx, &slices, client.InNamespace(d.Namespace), client.MatchingLabels{discovery.LabelServiceName: r.service.Name}) != nil {
		return r, errFleet
	}
	expected := map[types.UID]string{}
	for _, p := range r.pods {
		expected[p.UID] = p.Name
		for k, v := range r.service.Spec.Selector {
			if p.Labels[k] != v {
				return r, errFleet
			}
		}
	}
	covered := map[types.UID]bool{}
	for _, s := range slices.Items {
		if !ownedBy(s.OwnerReferences, "Service", r.service.UID) || !s.DeletionTimestamp.IsZero() {
			return r, errFleet
		}
		for _, e := range s.Endpoints {
			if e.Conditions.Terminating != nil && *e.Conditions.Terminating {
				return r, errFleet
			}
			if e.Conditions.Ready == nil || !*e.Conditions.Ready {
				return r, errFleet
			}
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" || expected[e.TargetRef.UID] != e.TargetRef.Name || e.TargetRef.UID == "" || (e.TargetRef.Namespace != "" && e.TargetRef.Namespace != d.Namespace) || len(e.Addresses) == 0 {
				return r, errFleet
			}
			covered[e.TargetRef.UID] = true
		}
		r.endpoints = append(r.endpoints, s)
	}
	if len(covered) != len(expected) {
		return r, errFleet
	}
	sort.Slice(r.endpoints, func(i, j int) bool { return r.endpoints[i].Name < r.endpoints[j].Name })
	sort.Slice(r.pods, func(i, j int) bool { return r.pods[i].UID < r.pods[j].UID })
	return r, nil
}

func (f *Fleet) Observe(ctx context.Context, d *Desired) (Evidence, error) {
	var out Evidence
	if d == nil || f.Dump == nil {
		return out, errUnsupported
	}
	before, err := f.roster(ctx)
	if err != nil {
		return out, err
	}
	for _, p := range before.pods {
		raw, err := f.Dump(ctx, p)
		if err != nil {
			return out, errUnsupported
		}
		if err = d.Verify(raw); err != nil {
			return out, err
		}
		out.PodUIDs = append(out.PodUIDs, p.UID)
	}
	after, err := f.roster(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		return Evidence{}, errFleet
	}
	out.DeploymentUID = before.deployment.UID
	out.ObservedAt = time.Now().UTC()
	return out, nil
}
