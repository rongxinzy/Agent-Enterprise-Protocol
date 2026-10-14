package gatewayrender

import (
	"encoding/json"
	"errors"
	"sort"
)

const (
	quotaURL     = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-quota@sha256:2684810410de2803200f21d4971fe30161a7c0060c39ccab3906f6bcd4b8a509"
	quotaAuthURL = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/key-auth@sha256:18587fac5be178a2260c37ed74eb7abb9ed9b24915d3e2f037b69983772a4700"
)

// RenderGatewayQuota renders the quota admin route and its guard plugins.
// Quota management uses a dedicated native key-auth route. An end user cannot
// become its admin by sending X-Mse-Consumer through the authorizer.
func RenderGatewayQuota(input Input, cfg NativeGatewayConfig, password, adminCredential string) ([]RenderedResource, error) {
	if adminCredential == "" || cfg.RedisService == "" {
		return nil, errors.New("native quota secrets unavailable")
	}
	suffix := ResourceSuffix(input.Deployment)
	name := "aep-quota-" + suffix
	path := "/" + name + "/v1/chat/completions/quota"
	ingresses := []string{name}
	for _, route := range input.Routes {
		if !route.Enabled {
			continue
		}
		strategy, err := StrategyFor(route)
		if err != nil {
			return nil, err
		}
		if strategy.Bucket() == BucketAnthropic {
			ingresses = append(ingresses, AnthropicResourceName(route.ModelID))
		} else {
			ingresses = append(ingresses, OpenAIResourceName(input.Deployment, route.ModelID))
		}
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
	return []RenderedResource{{Kind: ResourceGatewayNative, APIPath: IngressAPIPath(name), Body: string(body) + "\n"}, quotaPlugin, authPlugin}, nil
}

func nativePlugin(name, plugin, phase string, priority int, ingresses []string, config map[string]any) RenderedResource {
	body, _ := json.Marshal(map[string]any{"apiVersion": "extensions.higress.io/v1alpha1", "kind": "WasmPlugin", "metadata": map[string]any{"name": name, "namespace": "higress-system"}, "spec": map[string]any{"url": plugin, "failStrategy": "FAIL_CLOSE", "phase": phase, "priority": priority, "defaultConfigDisable": true, "matchRules": []any{map[string]any{"ingress": ingresses, "config": config, "configDisable": false}}}})
	return RenderedResource{Kind: ResourceGatewayNative, APIPath: "/apis/extensions.higress.io/v1alpha1/namespaces/higress-system/wasmplugins/" + name, Body: string(body) + "\n"}
}
