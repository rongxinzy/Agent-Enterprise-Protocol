package reconciler

import (
	"encoding/json"
	"errors"
	"sort"
)

// Quota management uses a dedicated native key-auth route. An end user cannot
// become its admin by sending X-Mse-Consumer through the authorizer.
func RenderGatewayQuota(desired DesiredState, cfg NativeGatewayConfig, password, adminCredential string) ([]RenderedResource, error) {
	if adminCredential == "" || cfg.RedisService == "" {
		return nil, errors.New("native quota secrets unavailable")
	}
	suffix := resourceSuffix(desired.TenantID())
	name := "aep-quota-" + suffix
	path := "/" + name + "/v1/chat/completions/quota"
	ingresses := []string{name}
	hasOpenAI := false
	for _, route := range desired.Routes {
		if !route.Enabled {
			continue
		}
		if route.Protocol == "anthropic" {
			ingresses = append(ingresses, anthropicResourceName(route.ModelID))
		} else {
			hasOpenAI = true
		}
	}
	if hasOpenAI {
		ingresses = append(ingresses, "aep-model-gateway-"+suffix)
	}
	sort.Strings(ingresses)
	redis := map[string]any{"service_name": cfg.RedisService, "service_port": cfg.RedisPort, "timeout": 1000, "database": cfg.RedisDatabase}
	if cfg.RedisUsername != "" {
		redis["username"] = cfg.RedisUsername
	}
	if password != "" {
		redis["password"] = password
	}
	quota := map[string]any{"admin_consumer": name, "admin_path": "/quota", "redis_key_prefix": "aep_quota:", "redis": redis, "enable_path_suffixes": []string{"/chat/completions", "/completions", "/responses", "/messages"}}
	auth := map[string]any{"global_auth": false, "in_header": true, "in_query": false, "keys": []string{"authorization"}, "consumers": []any{map[string]any{"name": name, "credential": "Bearer " + adminCredential}}}
	quotaPlugin := nativePlugin(name, quotaURL, "UNSPECIFIED_PHASE", 750, ingresses, quota)
	authPlugin := nativePlugin("aep-quota-auth-"+suffix, quotaAuthURL, "AUTHN", 310, []string{name}, auth)
	ingress := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": name, "namespace": "higress-system"}, "spec": map[string]any{"ingressClassName": "higress", "rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{"path": path, "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": "aep-model-gateway", "port": map[string]int{"number": 80}}}}}}}}}}
	body, _ := json.Marshal(ingress)
	return []RenderedResource{{Kind: ResourceGatewayNative, APIPath: ingressAPIPath(name), Body: string(body) + "\n"}, quotaPlugin, authPlugin}, nil
}

func nativePlugin(name, plugin, phase string, priority int, ingresses []string, config map[string]any) RenderedResource {
	body, _ := json.Marshal(map[string]any{"apiVersion": "extensions.higress.io/v1alpha1", "kind": "WasmPlugin", "metadata": map[string]any{"name": name, "namespace": "higress-system"}, "spec": map[string]any{"url": plugin, "failStrategy": "FAIL_CLOSE", "phase": phase, "priority": priority, "defaultConfigDisable": true, "matchRules": []any{map[string]any{"ingress": ingresses, "config": config, "configDisable": false}}}})
	return RenderedResource{Kind: ResourceGatewayNative, APIPath: "/apis/extensions.higress.io/v1alpha1/namespaces/higress-system/wasmplugins/" + name, Body: string(body) + "\n"}
}
