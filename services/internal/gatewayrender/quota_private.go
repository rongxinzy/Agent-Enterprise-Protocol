package gatewayrender

import "encoding/json"

// renderPrivateQuota composes native Istio routing/access policy and Higress
// plugins. AEP neither evaluates the key nor reads/calculates Redis balances.
func renderPrivateQuota(name, suffix, path string, ingresses []string, quota, auth map[string]any, credential string) []RenderedResource {
	const port = 18081
	host := name + ".higress-system.svc.cluster.local"
	domain := func(config map[string]any) map[string]any {
		return map[string]any{"domain": []string{host}, "configDisable": false, "config": config}
	}
	rules := []any{domain(quota)}
	if len(ingresses) > 0 {
		rules = append(rules, map[string]any{"ingress": ingresses, "configDisable": false, "config": quota})
	}
	// A route header match alone is insufficient: ai-quota can return a local
	// response before the router. Native DENY executes before quota even if the
	// separate optional key-auth plugin disappears. Scope by host AND port so
	// independent deployment keys do not deny one another on the same gateway.
	guard := privateQuotaObject("security.istio.io/v1", "AuthorizationPolicy", "authorizationpolicies", name+"-guard", map[string]any{
		"selector": map[string]any{"matchLabels": map[string]string{"app": "higress-gateway"}}, "action": "DENY",
		"rules": []any{map[string]any{
			"to":   []any{map[string]any{"operation": map[string]any{"ports": []string{"18081"}, "hosts": []string{host, host + ":*"}}}},
			"when": []any{map[string]any{"key": "request.headers[authorization]", "notValues": []string{"Bearer " + credential}}},
		}},
	})
	virtualService := privateQuotaObject("networking.istio.io/v1", "VirtualService", "virtualservices", name, map[string]any{
		"hosts": []string{host}, "gateways": []string{"higress-system/" + name}, "http": []any{map[string]any{
			"name": "quota-private", "match": []any{map[string]any{
				"uri": map[string]string{"prefix": path}, "headers": map[string]any{"authorization": map[string]string{"exact": "Bearer " + credential}},
			}}, "directResponse": map[string]int{"status": 404},
		}},
	})
	gate := privateQuotaObject("networking.istio.io/v1", "Gateway", "gateways", name, map[string]any{
		"selector": map[string]string{"app": "higress-gateway"}, "servers": []any{map[string]any{
			"port": map[string]any{"number": port, "name": "http-quota-private", "protocol": "HTTP"}, "hosts": []string{host},
		}},
	})
	service := privateQuotaObject("v1", "Service", "services", name, map[string]any{
		"type": "ClusterIP", "selector": map[string]string{"app": "higress-gateway"},
		"ports": []any{map[string]any{"name": "http-quota-private", "port": port, "targetPort": port}},
	})
	return []RenderedResource{guard,
		nativeMatchedPlugin("aep-quota-auth-"+suffix, quotaAuthURL, "AUTHN", 310, []any{domain(auth)}),
		nativeMatchedPlugin(name, quotaURL, "UNSPECIFIED_PHASE", 750, rules), virtualService, gate, service}
}

func privateQuotaObject(apiVersion, kind, plural, name string, spec map[string]any) RenderedResource {
	base := "/apis/" + apiVersion
	if apiVersion == "v1" {
		base = "/api/v1"
	}
	body, _ := json.Marshal(map[string]any{"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]string{"name": name, "namespace": "higress-system"}, "spec": spec})
	return RenderedResource{Kind: ResourceGatewayNative,
		APIPath: base + "/namespaces/higress-system/" + plural + "/" + name, Body: string(body) + "\n"}
}
