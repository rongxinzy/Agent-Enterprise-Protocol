package access

import (
	"regexp"

	gateway "github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const routeFinalizer = "gateway.aep.zhiyuan.io/access-route"
const routeOwner = "gateway.aep.zhiyuan.io/access-route-owner"
const routeLabel = "gateway.aep.zhiyuan.io/access-route"

func bindingRoute() *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion("networking.istio.io/v1alpha3")
	o.SetKind("EnvoyFilter")
	return o
}

// A selection route never duplicates a provider route. Native ext_authz clears
// the route cache after Authorino overwrites the binding hint and validated
// model header; the existing ModelRoute then owns provider/protocol/credentials.
// Without that successful handoff, the deliberately absent cluster fails closed.
func renderBindingRoute(g *gateway.ModelAccessGrant, host string) *unstructured.Unstructured {
	o := bindingRoute()
	o.SetNamespace("higress-system")
	o.SetName(g.Name)
	o.SetLabels(map[string]string{routeLabel: "true", routeOwner: string(g.UID)})
	o.SetAnnotations(map[string]string{BindingKey: bindingKey(g.Spec)})
	o.Object["spec"] = map[string]any{
		"workloadSelector": map[string]any{"labels": map[string]any{"app": "higress-gateway"}},
		"configPatches": []any{map[string]any{
			"applyTo": "HTTP_ROUTE",
			// Higress names this RDS resource explicitly; its HTTP_ROUTE patch
			// context does not populate Istio's routeConfiguration.portNumber.
			"match": map[string]any{"context": "GATEWAY", "routeConfiguration": map[string]any{"name": "higress-rds-80.*"}},
			"patch": map[string]any{"operation": "INSERT_BEFORE", "value": map[string]any{
				"name": g.Name,
				// Existing ModelRoute Ingresses use a wildcard virtual host. Match
				// the trusted deployment host in the route, not an invented vhost.
				"match": map[string]any{"prefix": "/v1/", "headers": []any{
					map[string]any{"name": ":authority", "string_match": map[string]any{"safe_regex": map[string]any{"regex": regexp.QuoteMeta(host) + "(:80)?"}}},
					map[string]any{"name": "x-aep-access-binding", "string_match": map[string]any{"exact": bindingKey(g.Spec)}},
				}},
				"route": map[string]any{"cluster": "aep-access-unrouted"},
				"typed_per_filter_config": map[string]any{"envoy.filters.http.ext_authz": map[string]any{
					"@type":          "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute",
					"check_settings": map[string]any{"context_extensions": map[string]any{"host": bindingKey(g.Spec) + ".access.aep.invalid"}},
				}},
			}},
		}},
	}
	return o
}
