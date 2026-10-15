package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/csaupgrade"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/dataplane"
	projection "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/internal/render"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

const (
	finalizer          = "gateway.aep.zhiyuan.io/rendered-resources"
	ownerLabel         = "gateway.aep.zhiyuan.io/owner-uid"
	snapshotAnnotation = "gateway.aep.zhiyuan.io/snapshot"
	fieldManager       = "aep-operator"
	driftInterval      = 30 * time.Second
)

var errPublicationPending = errors.New("publication inputs are incomplete or inconsistent")

// Runtime performs bounded, uncached Secret reads and SSA without forced
// takeover. The reader is the Manager's APIReader, not a Secret informer cache.
type Runtime struct {
	client.Client
	Reader                client.Reader
	CredentialNamespace   string
	NativeSecretNamespace string
	DataPlane             *dataplane.Fleet
}

type snapshot struct {
	deployment gateway.DeploymentConfig
	routes     []gateway.ModelRoute
	resources  []gatewayrender.RenderedResource
	hash       string
	forbidden  []string
}

func (r *Runtime) secret(ctx context.Context, namespace string, ref *gateway.CredentialReference) (string, error) {
	if ref == nil {
		return "", nil
	}
	var secret corev1.Secret
	if namespace == "" || r.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, &secret) != nil {
		return "", errors.New("referenced credential unavailable")
	}
	value := secret.Data[ref.Key]
	if len(value) == 0 {
		return "", errors.New("referenced credential unavailable")
	}
	return string(value), nil
}

func (r *Runtime) snapshot(ctx context.Context, key client.ObjectKey) (snapshot, error) {
	var out snapshot
	if err := r.Reader.Get(ctx, key, &out.deployment); err != nil {
		return out, errors.New("deployment configuration unavailable")
	}
	if !out.deployment.DeletionTimestamp.IsZero() {
		return out, errors.New("deployment configuration is terminating")
	}
	var routes gateway.ModelRouteList
	var policies gateway.ModelPolicyList
	if err := r.Reader.List(ctx, &routes, client.InNamespace(key.Namespace)); err != nil {
		return out, errors.New("route snapshot unavailable")
	}
	if err := r.Reader.List(ctx, &policies, client.InNamespace(key.Namespace)); err != nil {
		return out, errors.New("policy snapshot unavailable")
	}
	if err := consistentPublication(&out.deployment, routes.Items, policies.Items); err != nil {
		return out, err
	}
	sort.Slice(routes.Items, func(i, j int) bool { return routes.Items[i].Name < routes.Items[j].Name })
	sort.Slice(policies.Items, func(i, j int) bool { return policies.Items[i].Name < policies.Items[j].Name })
	input := gatewayrender.Input{Deployment: out.deployment.Spec.DeploymentID, Revision: "cr"}
	byName := map[string]gateway.ModelRoute{}
	models := map[string]bool{}
	credentials := map[string]string{}
	for _, route := range routes.Items {
		byName[route.Name] = route

		if !route.Spec.Enabled || !route.DeletionTimestamp.IsZero() {
			name := gatewayrender.OpenAIResourceName(out.deployment.Spec.DeploymentID, route.Spec.ModelID)
			if route.Spec.Protocol == "anthropic" {
				name = gatewayrender.AnthropicResourceName(route.Spec.ModelID)
			}
			out.forbidden = append(out.forbidden, name)
		}
		if !route.DeletionTimestamp.IsZero() {
			continue
		}
		if err := validateModelRoute(&route); err != nil {
			return out, errors.New("route spec invalid")
		}
		if models[route.Spec.ModelID] {
			return out, errors.New("duplicate model identifier")
		}
		models[route.Spec.ModelID] = true
		out.routes = append(out.routes, route)
		projected := projection.RouteFromCR(route)
		input.Routes = append(input.Routes, projected)
		if route.Spec.Enabled && route.Spec.CredentialRef != nil {
			value, err := r.secret(ctx, r.CredentialNamespace, route.Spec.CredentialRef)
			if err != nil {
				return out, err
			}
			credentials[route.Spec.CredentialRef.Name+"/"+route.Spec.CredentialRef.Key] = value
		}
	}
	activePolicies := make([]gateway.ModelPolicy, 0)
	for _, policy := range policies.Items {
		if policy.Spec.Disabled && policy.Spec.NativeRule != nil {
			out.forbidden = append(out.forbidden, "aep-limit-"+gatewayrender.ResourceSuffix(out.deployment.Spec.DeploymentID+"/"+policy.Spec.NativeRule.ID))
		}
		if policy.DeletionTimestamp.IsZero() {
			activePolicies = append(activePolicies, policy)
		}
	}
	publication, err := projection.Limits(activePolicies, byName)
	if err != nil {
		return out, err
	}
	_, out.resources, _, err = gatewayrender.Render(input, credentials)
	if err != nil {
		return out, errors.New("route rendering failed")
	}
	native := out.deployment.Spec.NativeGateway
	if len(publication.Items) > 0 && (native == nil || !native.Enabled) {
		return out, errors.New("native policies require an enabled Redis-backed gateway")
	}
	if native != nil && native.Enabled {
		cfg := gatewayrender.NativeGatewayConfig{Enabled: true, RedisService: native.RedisService, RedisPort: native.RedisPort, RedisUsername: native.RedisUsername, RedisDatabase: native.RedisDatabase}
		password, err := r.secret(ctx, r.NativeSecretNamespace, native.RedisPasswordRef)
		if err != nil {
			return out, err
		}
		limits, err := gatewayrender.RenderGatewayLimits(input, publication, cfg, password)
		if err != nil {
			return out, errors.New("native limit rendering failed")
		}
		out.resources = append(out.resources, limits...)
		if native.QuotaAdminCredentialRef != nil {
			credential, err := r.secret(ctx, r.NativeSecretNamespace, native.QuotaAdminCredentialRef)
			if err != nil {
				return out, err
			}
			quota, err := gatewayrender.RenderGatewayQuota(input, cfg, password, credential)
			if err != nil {
				return out, errors.New("native quota rendering failed")
			}
			out.resources = append(out.resources, quota...)
		}
	}
	// Include only admitted specs, identities and rendered bytes (including
	// resolved Secret changes); never log or expose the preimage.
	data, _ := json.Marshal(struct {
		UID       types.UID
		Spec      gateway.DeploymentConfigSpec
		Routes    []gateway.ModelRouteSpec
		Policies  []gateway.ModelPolicySpec
		Resources []gatewayrender.RenderedResource
	}{
		UID: out.deployment.UID, Spec: out.deployment.Spec, Routes: routeSpecs(out.routes), Policies: policySpecs(activePolicies), Resources: out.resources,
	})
	sum := sha256.Sum256(data)
	out.hash = hex.EncodeToString(sum[:])
	return out, nil
}

func routeSpecs(routes []gateway.ModelRoute) []gateway.ModelRouteSpec {
	out := make([]gateway.ModelRouteSpec, 0, len(routes))
	for _, route := range routes {
		out = append(out, route.Spec)
	}
	return out
}
func policySpecs(policies []gateway.ModelPolicy) []gateway.ModelPolicySpec {
	out := make([]gateway.ModelPolicySpec, 0, len(policies))
	for _, policy := range policies {
		out = append(out, policy.Spec)
	}
	return out
}

func (r *Runtime) ensureFinalizer(ctx context.Context, obj client.Object) (bool, error) {
	if controllerutil.ContainsFinalizer(obj, finalizer) {
		return false, nil
	}
	base := obj.DeepCopyObject().(client.Object)
	controllerutil.AddFinalizer(obj, finalizer)
	return true, r.Patch(ctx, obj, client.MergeFrom(base))
}

func object(resource gatewayrender.RenderedResource) (*unstructured.Unstructured, error) {
	decoded := map[string]any{}
	if err := yaml.Unmarshal([]byte(resource.Body), &decoded); err != nil {
		return nil, errors.New("invalid rendered object")
	}
	obj := &unstructured.Unstructured{Object: decoded}
	if obj.GetNamespace() != "higress-system" || obj.GetName() == "" {
		return nil, errors.New("rendered object outside gateway namespace")
	}
	switch obj.GroupVersionKind() {
	case schema.GroupVersionKind{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
		schema.GroupVersionKind{Group: "networking.istio.io", Version: "v1alpha3", Kind: "EnvoyFilter"},
		schema.GroupVersionKind{Group: "extensions.higress.io", Version: "v1alpha1", Kind: "WasmPlugin"}:
	default:
		return nil, errors.New("unsupported rendered kind")
	}
	return obj, nil
}

// Apply all replacements before deleting obsolete objects. Inventory comes
// from live ownership labels, so a crash after SSA but before status is safe.
func (r *Runtime) apply(ctx context.Context, owner client.Object, resources []gatewayrender.RenderedResource, hash string) ([]gateway.AppliedResource, error) {
	if owner.GetUID() == "" {
		return nil, errors.New("owner identity unavailable")
	}
	inventory := make([]gateway.AppliedResource, 0, len(resources))
	keep := map[string]bool{}
	for _, resource := range resources {
		obj, err := object(resource)
		if err != nil {
			return nil, err
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GroupVersionKind())
		err = r.Reader.Get(ctx, client.ObjectKeyFromObject(obj), live)
		missing := apierrors.IsNotFound(err)
		if err == nil {
			if live.GetLabels()[ownerLabel] != string(owner.GetUID()) {
				return nil, errors.New("resource ownership conflict")
			}
			obj.SetResourceVersion(live.GetResourceVersion())
		} else if !apierrors.IsNotFound(err) {
			return nil, errors.New("resource read failed")
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[ownerLabel] = string(owner.GetUID())
		obj.SetLabels(labels)
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[snapshotAnnotation] = hash
		if event := owner.GetAnnotations()["gateway.aep.zhiyuan.io/audit-event-id"]; event != "" {
			annotations["gateway.aep.zhiyuan.io/audit-event-id"] = event
		}
		obj.SetAnnotations(annotations)
		// A not-found read is not an atomic creation guard. Create cannot
		// overwrite an object that appeared since that read; subsequent writes
		// use SSA with the verified resourceVersion and no ForceOwnership.
		if missing {
			live = obj.DeepCopy()
			if err := r.Create(ctx, live, client.FieldOwner(fieldManager)); err != nil {
				return nil, errors.New("resource creation failed: " + string(apierrors.ReasonForError(err)))
			}
		}
		// Create records an Update manager. Use client-go's native migration
		// to promote only our own fields to Apply, retaining other managers.
		// The generated patch includes an optimistic resourceVersion guard.
		// Repeating it for owned objects also repairs a crash after creation.
		upgrade, err := csaupgrade.UpgradeManagedFieldsPatch(live, sets.New(fieldManager), fieldManager)
		if err != nil {
			return nil, errors.New("field ownership migration failed")
		}
		if len(upgrade) > 0 {
			if err := r.Patch(ctx, live, client.RawPatch(types.JSONPatchType, upgrade), client.FieldOwner(fieldManager)); err != nil {
				return nil, errors.New("field ownership migration failed: " + string(apierrors.ReasonForError(err)))
			}
		}
		obj.SetResourceVersion(live.GetResourceVersion())
		if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj), client.FieldOwner(fieldManager)); err != nil {
			return nil, errors.New("server-side apply failed: " + string(apierrors.ReasonForError(err)))
		}
		ref := gateway.AppliedResource{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
		inventory = append(inventory, ref)
		keep[resourceKey(obj)] = true
	}
	if err := r.prune(ctx, owner.GetUID(), keep); err != nil {
		return nil, err
	}
	return inventory, nil
}

func resourceKey(obj *unstructured.Unstructured) string {
	return obj.GetAPIVersion() + "/" + obj.GetKind() + "/" + obj.GetName()
}

var renderedGVKs = []schema.GroupVersionKind{
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
	{Group: "networking.istio.io", Version: "v1alpha3", Kind: "EnvoyFilter"},
	{Group: "extensions.higress.io", Version: "v1alpha1", Kind: "WasmPlugin"},
}

func (r *Runtime) prune(ctx context.Context, uid types.UID, keep map[string]bool) error {
	if uid == "" {
		return errors.New("owner identity unavailable")
	}
	for _, gvk := range renderedGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.Reader.List(ctx, list, client.InNamespace("higress-system"), client.MatchingLabels{ownerLabel: string(uid)}); err != nil {
			return errors.New("owned resource inventory unavailable")
		}
		for i := range list.Items {
			obj := &list.Items[i]
			obj.SetGroupVersionKind(gvk)
			if obj.GetLabels()[ownerLabel] != string(uid) || keep[resourceKey(obj)] {
				continue
			}
			// Protect against a delete/recreate race after the inventory read.
			objectUID, rv := obj.GetUID(), obj.GetResourceVersion()
			if err := r.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &objectUID, ResourceVersion: &rv}}); err != nil && !apierrors.IsNotFound(err) {
				return errors.New("owned resource cleanup failed")
			}
		}
	}
	return nil
}

func (r *Runtime) finalize(ctx context.Context, obj client.Object) error {
	if !controllerutil.ContainsFinalizer(obj, finalizer) {
		return nil
	}
	if err := r.prune(ctx, obj.GetUID(), nil); err != nil {
		return err
	}
	base := obj.DeepCopyObject().(client.Object)
	controllerutil.RemoveFinalizer(obj, finalizer)
	return r.Patch(ctx, obj, client.MergeFrom(base))
}
