package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

const SemanticHash = "gateway.aep.zhiyuan.io/access-semantic-hash"
const BindingKey = "gateway.aep.zhiyuan.io/access-binding"
const PolicyLabel = "gateway.aep.zhiyuan.io/access-policy"

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func digest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func bindingKey(spec gateway.ModelAccessGrantSpec) string {
	return digest(struct {
		DeploymentID string                     `json:"deploymentId"`
		Subject      gateway.ModelAccessSubject `json:"subject"`
		Binding      gateway.ModelAccessBinding `json:"binding"`
	}{spec.DeploymentID, spec.Subject, spec.Binding})
}

// Validate supplements API admission with exact relay metadata correlation.
// The DB writer owns canonical source-byte validation; the native semantic hash
// below is independently derived from the configuration actually compiled.
func Validate(grant *gateway.ModelAccessGrant) error {
	p := grant.Spec.Publication
	op, err := uuid.Parse(p.OperationID)
	key := bindingKey(grant.Spec)
	sum := sha256.Sum256([]byte(grant.Spec.DeploymentID))
	if err != nil || op.String() != p.OperationID || p.Sequence < 1 || !hashPattern.MatchString(p.ContentHash) ||
		p.Revision == "" || len(p.Revision) > 200 || grant.UID == "" || grant.Namespace != "aep-system" ||
		grant.Spec.DeploymentID == "" || grant.Spec.Subject.ID == "" ||
		(grant.Spec.Subject.Kind != "human" && grant.Spec.Subject.Kind != "agent") ||
		grant.Spec.IssuedAt.IsZero() || !grant.Spec.ValidUntil.After(grant.Spec.IssuedAt.Time) ||
		grant.Spec.ValidUntil.Sub(grant.Spec.IssuedAt.Time) > time.Hour ||
		grant.Name != "access-"+key || grant.Annotations[BindingKey] != key ||
		grant.Labels[gateway.PublicationDeploymentLabel] != hex.EncodeToString(sum[:16]) {
		return ErrInput
	}
	for key, value := range markers(p) {
		if grant.Annotations[key] != value {
			return ErrInput
		}
	}
	return nil
}

func markers(p gateway.PublicationReference) map[string]string {
	return map[string]string{gateway.PublicationSequence: strconv.FormatInt(p.Sequence, 10),
		gateway.PublicationOperationID: p.OperationID, gateway.PublicationRevision: p.Revision,
		gateway.PublicationContentHash: p.ContentHash}
}

func authConfig() *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion("authorino.kuadrant.io/v1beta3")
	o.SetKind("AuthConfig")
	return o
}

func jsonValue(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// Render compiles an admitted grant into a stable AuthConfig and a
// separately changing freshness file. Renewal MUST NOT update AuthConfig at all.
// No Secret or provider value is needed. Workload authentication is opt-in.
func Render(grant *gateway.ModelAccessGrant, options Options) (*unstructured.Unstructured, *corev1.ConfigMap, error) {
	if Validate(grant) != nil || options.Validate() != nil || !validNativeBinding(grant.Spec, options) ||
		grant.Spec.Disabled && len(grant.Spec.Models) != 0 {
		return nil, nil, ErrInput
	}
	spec := grant.DeepCopy().Spec
	spec.Publication = gateway.PublicationReference{}
	// Live Pod membership is checked only by the uncached, freshness-bounded
	// native policy. Pod churn must not rebuild authentication/decision caches.
	spec.WorkloadPodUIDs = nil
	spec.IssuedAt, spec.ValidUntil = metav1.Time{}, metav1.Time{}
	slices.Sort(spec.Models)
	slices.Sort(spec.RoleIDs)
	slices.Sort(spec.TeamIDs)
	slices.SortFunc(spec.Licenses, func(a, b gateway.ModelAccessLicense) int { return strings.Compare(a.ID, b.ID) })
	hash := digest(struct {
		Spec    gateway.ModelAccessGrantSpec
		Options Options
	}{spec, options})
	name := grant.Name
	meta := metav1.ObjectMeta{Name: name, Namespace: grant.Namespace,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: gateway.GroupVersion.String(), Kind: "ModelAccessGrant",
			Name: name, UID: grant.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(false)}},
		Labels: map[string]string{PolicyLabel: "true"}, Annotations: markers(grant.Spec.Publication)}
	meta.Annotations[SemanticHash] = hash
	config := authConfig()
	config.SetName(name)
	config.SetNamespace(grant.Namespace)
	config.SetLabels(meta.Labels)
	config.SetAnnotations(meta.Annotations)
	config.SetOwnerReferences(meta.OwnerReferences)
	// One year is a cache retention ceiling, not permission/token validity.
	// Short JWT and publication deadlines are enforced separately per request.
	cache := map[string]any{"key": map[string]any{"selector": "context.request.http.headers.authorization"}, "ttl": int64(31536000)}
	config.Object["spec"] = map[string]any{
		"hosts": []any{bindingKey(grant.Spec) + ".access.aep.invalid"},
		"authentication": map[string]any{"session": map[string]any{
			"jwt": map[string]any{"issuerUrl": options.IssuerURL}, "cache": cache, "priority": int64(0), "metrics": false}},
		"authorization": map[string]any{
			"access": map[string]any{"opa": map[string]any{"rego": accessRego(spec, hash, options), "allValues": true}, "cache": cache, "priority": int64(0), "metrics": false},
			"bounds": map[string]any{"priority": int64(1), "metrics": false, "opa": map[string]any{"externalPolicy": map[string]any{
				"url": strings.TrimRight(options.PolicyBaseURL, "/") + "/" + name + ".rego", "ttl": options.RefreshSeconds,
				"method": "GET", "contentType": "application/x-www-form-urlencoded"}, "allValues": true}},
		},
		"response": map[string]any{"success": map[string]any{"headers": map[string]any{"x-aep-access-observation": map[string]any{
			"priority": int64(0), "metrics": false,
			"json": map[string]any{"properties": map[string]any{
				"semanticHash": map[string]any{"selector": "auth.authorization.access.policy_hash"},
				"publication":  map[string]any{"selector": "auth.authorization.bounds.publication"},
			}}}}}},
	}
	bounds := boundsRego(grant.Spec, hash)
	if spec.Binding.Kind == "kubernetes" {
		projection := config.Object["spec"].(map[string]any)
		projection["authentication"] = map[string]any{"workload": map[string]any{
			"kubernetesTokenReview": map[string]any{"audiences": []any{options.WorkloadAudience}},
			"cache":                 cache, "priority": int64(0), "metrics": false}}
		projection["authorization"].(map[string]any)["access"].(map[string]any)["opa"].(map[string]any)["rego"] = workloadAccessRego(spec, hash)
		bounds = workloadBoundsRego(grant.Spec, hash)
	}
	if options.IngressHost != "" {
		identity, err := ingressIdentity(spec)
		if err != nil {
			return nil, nil, ErrInput
		}
		success := config.Object["spec"].(map[string]any)["response"].(map[string]any)["success"].(map[string]any)
		success["dynamicMetadata"] = map[string]any{"aep_trusted_headers": map[string]any{
			"plain": map[string]any{"selector": "auth.authorization.bounds.trusted_headers"}, "metrics": false, "priority": int64(0)}}
		headers := config.Object["spec"].(map[string]any)["response"].(map[string]any)["success"].(map[string]any)["headers"].(map[string]any)
		// Both fields overwrite caller input. Re-routing is safe only after the
		// actual body model has passed scope, current-policy and expiry checks.
		headers["x-aep-access-binding"] = map[string]any{"plain": map[string]any{"value": "authorized"}, "metrics": false, "priority": int64(0)}
		headers["x-aep-model-id"] = map[string]any{"plain": map[string]any{"selector": "auth.authorization.bounds.requested_model"}, "metrics": false, "priority": int64(0)}
		bounds += identity
	}
	cm := &corev1.ConfigMap{ObjectMeta: *meta.DeepCopy(), Data: map[string]string{name + ".rego": bounds}}
	// Native metadata carries the sequence of the last semantic write. The
	// reconciler preserves it on renewal, avoiding upstream cache reconstruction.
	return config, cm, nil
}
