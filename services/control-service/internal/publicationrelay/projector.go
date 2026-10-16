// Package publicationrelay projects audited publication snapshots to AEP CRs.
// It never renders Higress resources, resolves Secrets, or reports readiness.
package publicationrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"sort"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/control-service/internal/repository"
	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewayrender"
)

const (
	ownerLabel       = gateway.PublicationDeploymentLabel
	sequenceKey      = gateway.PublicationSequence
	operationKey     = gateway.PublicationOperationID
	hashKey          = gateway.PublicationContentHash
	revisionKey      = gateway.PublicationRevision
	auditResourceKey = "gateway.aep.zhiyuan.io/audit-resource-id"
)

var (
	ErrSuperseded = errors.New("newer CR publication exists")
	ErrOwnership  = errors.New("CR publication ownership conflict")
	ErrSnapshot   = errors.New("snapshot cannot be represented by operator CRs")
)

type Projector struct {
	Client    client.Client // Direct API client; never a cache or a Secret reader.
	Namespace string
}

type snapshot struct {
	Revision      string                 `json:"revision"`
	Routes        []gatewayrender.Route  `json:"routes"`
	GatewayLimits *[]gatewaypolicy.Limit `json:"gatewayLimits,omitempty"`
}

func owner(deployment string) string {
	sum := sha256.Sum256([]byte(deployment))
	return hex.EncodeToString(sum[:16])
}

func markers(item repository.PublicationDelivery) map[string]string {
	return map[string]string{sequenceKey: strconv.FormatInt(item.Sequence, 10), operationKey: item.OperationID,
		hashKey: item.ContentHash, revisionKey: item.Revision, auditResourceKey: item.OperationID}
}

// checkMarker plus resourceVersion is an optimistic fence. Comparing the
// sequence alone would leave a read/write race; a stale Update cannot pass
// Kubernetes' resourceVersion precondition after the newer write commits.
func checkMarker(obj client.Object, item repository.PublicationDelivery, required bool) error {
	label := obj.GetLabels()[ownerLabel]
	if label == "" && !required && obj.GetAnnotations()[sequenceKey] == "" {
		return nil // Only the pre-provisioned DeploymentConfig may be unmarked.
	}
	if label != owner(item.DeploymentID) || !obj.GetDeletionTimestamp().IsZero() {
		return ErrOwnership
	}
	sequence, err := strconv.ParseInt(obj.GetAnnotations()[sequenceKey], 10, 64)
	if err != nil || sequence < 1 {
		return ErrOwnership
	}
	if sequence > item.Sequence {
		return ErrSuperseded
	}
	if sequence == item.Sequence && (obj.GetAnnotations()[operationKey] != item.OperationID || obj.GetAnnotations()[hashKey] != item.ContentHash) {
		return ErrOwnership
	}
	return nil
}

func mark(obj client.Object, item repository.PublicationDelivery) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ownerLabel] = owner(item.DeploymentID)
	obj.SetLabels(labels)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for key, value := range markers(item) {
		annotations[key] = value
	}
	obj.SetAnnotations(annotations)
}

func routeSpec(route gatewayrender.Route) (gateway.ModelRouteSpec, error) {
	u, err := url.Parse(route.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return gateway.ModelRouteSpec{}, ErrSnapshot
	}
	protocol := "openai"
	switch route.Protocol {
	case "anthropic":
		protocol = "anthropic"
	case "openai-compatible":
	default:
		return gateway.ModelRouteSpec{}, ErrSnapshot
	}
	value := gateway.ModelRouteSpec{DeploymentRef: "deployment", ModelID: route.ModelID, Protocol: protocol,
		ProviderType: route.ProviderType, Enabled: route.Enabled, Upstream: gateway.ModelRouteUpstream{Endpoint: route.Endpoint, Model: route.UpstreamModel}}
	if protocol == "openai" && value.ProviderType == "" {
		value.ProviderType = "openai"
	}
	if protocol == "anthropic" && value.ProviderType != "" {
		return gateway.ModelRouteSpec{}, ErrSnapshot
	}
	if route.CredentialRef != nil {
		if route.CredentialRef.Namespace != nil {
			return gateway.ModelRouteSpec{}, ErrSnapshot
		}
		value.CredentialRef = &gateway.CredentialReference{Name: route.CredentialRef.Name, Key: route.CredentialRef.Key}
	}
	return value, nil
}

// desiredRoutes is shared by delivery and evidence validation. Neither path
// reconstructs intent from the mutable catalog or reads credentials.
func desiredRoutes(item repository.PublicationDelivery) (map[string]gateway.ModelRouteSpec, error) {
	var current snapshot
	var history []gatewayrender.Route
	if item.Sequence < 1 || json.Unmarshal(item.Payload, &current) != nil || json.Unmarshal(item.HistoricalRoutes, &history) != nil || current.Revision != item.Revision {
		return nil, ErrSnapshot
	}
	// The established content hash is SHA256 of canonical {revision,routes}.
	canonical, _ := json.Marshal(current)
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != item.ContentHash {
		return nil, ErrSnapshot
	}
	desired := map[string]gateway.ModelRouteSpec{}
	for _, route := range history {
		route.Enabled = false
		value, err := routeSpec(route)
		if err != nil {
			continue
		} // An unrepresentable old route could never have been created.
		desired[route.ModelID] = value
	}
	seen := map[string]bool{}
	for _, route := range current.Routes {
		if seen[route.ModelID] {
			return nil, ErrSnapshot
		}
		seen[route.ModelID] = true
		value, err := routeSpec(route)
		if err != nil {
			return nil, err
		}
		desired[route.ModelID] = value
	}
	return desired, nil
}

// Project retains disabled tombstones to fence old creates, then reads back
// all written specs and markers. This is delivery evidence, never readiness.
func (p *Projector) Project(ctx context.Context, item repository.PublicationDelivery) error {
	desired, err := desiredRoutes(item)
	if err != nil {
		return err
	}
	if _, err := desiredPolicies(item); err != nil {
		return err
	}
	var deployment gateway.DeploymentConfig
	key := client.ObjectKey{Namespace: p.Namespace, Name: "deployment"}
	if err := p.Client.Get(ctx, key, &deployment); err != nil {
		return err
	}
	if deployment.Spec.DeploymentID != item.DeploymentID || !deployment.DeletionTimestamp.IsZero() {
		return ErrOwnership
	}
	if err := checkMarker(&deployment, item, false); err != nil {
		return err
	}
	// List a consistent namespace snapshot once per attempt. Repeated Gets of
	// every already-delivered route could exhaust the bounded API budget before
	// reaching unfinished routes. Persisted CR markers are our retry checkpoints.
	var routes gateway.ModelRouteList
	if err := p.Client.List(ctx, &routes, client.InNamespace(p.Namespace)); err != nil {
		return err
	}
	liveRoutes := indexRoutes(&routes)
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := p.writeRoute(ctx, item, id, desired[id], liveRoutes["model-"+id]); err != nil {
			return err
		}
	}
	// Re-read all CRs after writes, including tombstones. A success response
	// or a lost response alone is not sufficient to acknowledge projection.
	if err := p.Client.List(ctx, &routes, client.InNamespace(p.Namespace)); err != nil {
		return err
	}
	liveRoutes = indexRoutes(&routes)
	for _, id := range ids {
		live := liveRoutes["model-"+id]
		if live == nil {
			return apierrors.NewNotFound(schema.GroupResource{Group: gateway.GroupVersion.Group, Resource: "modelroutes"}, "model-"+id)
		}
		if err := checkMarker(live, item, true); err != nil {
			return err
		}
		if live.Annotations[sequenceKey] != strconv.FormatInt(item.Sequence, 10) || !reflect.DeepEqual(live.Spec, desired[id]) {
			return ErrOwnership
		}
	}
	if err := p.projectPolicies(ctx, item); err != nil {
		return err
	}
	// Stamp the aggregate only after all model writes have been verified. The
	// fresh read preserves native gateway settings and concurrent status writes.
	if err := p.Client.Get(ctx, key, &deployment); err != nil {
		return err
	}
	if deployment.Spec.DeploymentID != item.DeploymentID {
		return ErrOwnership
	}
	if err := checkMarker(&deployment, item, false); err != nil {
		return err
	}
	before := deployment.DeepCopy()
	mark(&deployment, item)
	if !reflect.DeepEqual(before.ObjectMeta, deployment.ObjectMeta) {
		if err := p.Client.Update(ctx, &deployment); err != nil {
			return err
		}
	}
	var confirmed gateway.DeploymentConfig
	if err := p.Client.Get(ctx, key, &confirmed); err != nil {
		return err
	}
	if err := checkMarker(&confirmed, item, true); err != nil {
		return err
	}
	if confirmed.Annotations[sequenceKey] != strconv.FormatInt(item.Sequence, 10) {
		return ErrOwnership
	}
	return nil
}

func indexRoutes(routes *gateway.ModelRouteList) map[string]*gateway.ModelRoute {
	indexed := make(map[string]*gateway.ModelRoute, len(routes.Items))
	for i := range routes.Items {
		indexed[routes.Items[i].Name] = &routes.Items[i]
	}
	return indexed
}

func (p *Projector) writeRoute(ctx context.Context, item repository.PublicationDelivery, id string, spec gateway.ModelRouteSpec, live *gateway.ModelRoute) error {
	key := client.ObjectKey{Namespace: p.Namespace, Name: "model-" + id}
	if live == nil {
		obj := &gateway.ModelRoute{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}, Spec: spec}
		mark(obj, item)
		// Atomic Create refuses takeover after a not-found/create race.
		return p.Client.Create(ctx, obj, client.FieldOwner("aep-publication-relay"))
	}
	if err := checkMarker(live, item, true); err != nil {
		return err
	}
	before := live.DeepCopy()
	live.Spec = spec
	mark(live, item)
	if reflect.DeepEqual(before.Spec, live.Spec) && reflect.DeepEqual(before.ObjectMeta, live.ObjectMeta) {
		return nil
	}
	return p.Client.Update(ctx, live, client.FieldOwner("aep-publication-relay"))
}
