package gatewayrender

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

const (
	requestLimitURL = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/cluster-key-rate-limit@sha256:8f704ae666222d1cc8e67ea72a7342ba5f08db1dd1cb8f5344e3700924f87ce5"
	// Higress plugin snapshot 2.2.5, plugin version 2.0.3, source
	// 2b837c0ada8dbfb3e4bd92fc3f18ea532d269cb1. Older 2025 artifacts lack
	// global_threshold and Anthropic quota path/usage support.
	tokenLimitURL = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-token-ratelimit@sha256:9276a7d4cbd7afef668fd1aaf41212e663fd7fa98661c619398a7f2fb2736679"
)

// NativeGatewayConfig holds the deployment-level Redis/quota settings the
// native limit and quota plugins render against.
type NativeGatewayConfig struct {
	Enabled                 bool
	RedisService            string
	RedisPort               int
	RedisUsername           string
	RedisDatabase           int
	RedisPasswordRef        *SecretReference
	QuotaAdminCredentialRef *SecretReference
}

// RenderGatewayLimits renders one WasmPlugin per published limit rule,
// matching every enabled route ingress of the deployment.
func RenderGatewayLimits(input Input, publication gatewaypolicy.Publication, cfg NativeGatewayConfig, password string) ([]RenderedResource, error) {
	if cfg.RedisService == "" || cfg.RedisPort < 1 || cfg.RedisPort > 65535 || cfg.RedisDatabase < 0 {
		return nil, errors.New("native gateway Redis is not configured")
	}
	if publication.Revision != gatewaypolicy.Revision(publication.Items) {
		return nil, errors.New("native gateway publication hash mismatch")
	}
	items := append([]gatewaypolicy.Limit(nil), publication.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	resources := make([]RenderedResource, 0, len(items))
	ingresses := make([]string, 0)
	for _, route := range input.Routes {
		if !route.Enabled {
			continue
		}
		if route.Protocol == "anthropic" {
			ingresses = append(ingresses, AnthropicResourceName(route.ModelID))
		} else {
			ingresses = append(ingresses, OpenAIResourceName(input.Deployment, route.ModelID))
		}
	}
	sort.Strings(ingresses)
	for _, item := range items {
		if !gatewaypolicy.ValidID(item.ID) || !item.Configuration.Valid() {
			return nil, errors.New("invalid native gateway rule")
		}
		name := "aep-limit-" + ResourceSuffix(input.Deployment+"/"+item.ID)
		plugin, priority, field := requestLimitURL, 20, "query_per_"
		if item.Configuration.Kind == "tokens" {
			plugin, priority, field = tokenLimitURL, 600, "token_per_"
		}
		var out strings.Builder
		out.WriteString("apiVersion: extensions.higress.io/v1alpha1\nkind: WasmPlugin\nmetadata:\n  name: " + yamlScalar(name) + "\n  namespace: higress-system\nspec:\n  url: " + yamlScalar(plugin) + "\n  failStrategy: FAIL_CLOSE\n  phase: UNSPECIFIED_PHASE\n  priority: " + fmt.Sprint(priority) + "\n  defaultConfigDisable: true\n")
		if !item.Configuration.Enabled || len(ingresses) == 0 {
			out.WriteString("  matchRules: []\n")
		} else {
			redis := map[string]any{"service_name": cfg.RedisService, "service_port": cfg.RedisPort, "timeout": 1000, "database": cfg.RedisDatabase}
			if cfg.RedisUsername != "" {
				redis["username"] = cfg.RedisUsername
			}
			if password != "" {
				redis["password"] = password
			}
			config := map[string]any{"rule_name": name, "redis": redis, "rejected_code": 429}
			threshold := map[string]any{field + item.Configuration.Interval: item.Configuration.Maximum}
			if item.Configuration.ScopeType == "global" && item.Configuration.ModelID == nil {
				config["global_threshold"] = threshold
			} else {
				threshold["key"] = "1"
				config["rule_items"] = []any{map[string]any{"limit_by_header": gatewaypolicy.Header(item.Configuration), "limit_keys": []any{threshold}}}
			}
			encoded, _ := json.Marshal(config)
			out.WriteString("  matchRules:\n    - config: " + string(encoded) + "\n      configDisable: false\n      ingress:\n")
			for _, ingress := range ingresses {
				out.WriteString("        - " + yamlScalar(ingress) + "\n")
			}
		}
		resources = append(resources, RenderedResource{Kind: ResourceGatewayNative, APIPath: "/apis/extensions.higress.io/v1alpha1/namespaces/higress-system/wasmplugins/" + name, Body: out.String()})
	}
	return resources, nil
}
