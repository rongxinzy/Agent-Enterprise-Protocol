package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/rongxinzy/Agent-Enterprise-Protocol/services/internal/gatewaypolicy"
)

const (
	requestLimitURL = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/cluster-key-rate-limit@sha256:8f704ae666222d1cc8e67ea72a7342ba5f08db1dd1cb8f5344e3700924f87ce5"
	tokenLimitURL   = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-token-ratelimit@sha256:4296bf8d213f4558ca96a0dd380aebc9d18071f5ff3ac0fa67089208d1b52fad"
	quotaURL        = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-quota@sha256:21c1ea624d316d9baf8de8ba3fa9ddc8ff3492e28f4e227fbfaa7d8f12bc5922"
	quotaAuthURL    = "oci://higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/key-auth@sha256:18587fac5be178a2260c37ed74eb7abb9ed9b24915d3e2f037b69983772a4700"
)

type NativeGatewayConfig struct {
	Enabled                 bool
	RedisService            string
	RedisPort               int
	RedisUsername           string
	RedisDatabase           int
	RedisPasswordRef        *SecretReference
	QuotaAdminCredentialRef *SecretReference
}

func RenderGatewayLimits(desired DesiredState, publication gatewaypolicy.Publication, cfg NativeGatewayConfig, password string) ([]RenderedResource, error) {
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
	openAI := false
	for _, route := range desired.Routes {
		if !route.Enabled {
			continue
		}
		if route.Protocol == "anthropic" {
			ingresses = append(ingresses, anthropicResourceName(route.ModelID))
		} else {
			openAI = true
		}
	}
	if openAI {
		ingresses = append(ingresses, "aep-model-gateway-"+resourceSuffix(desired.TenantID()))
	}
	sort.Strings(ingresses)
	for _, item := range items {
		if !gatewaypolicy.ValidID(item.ID) || !item.Configuration.Valid() {
			return nil, errors.New("invalid native gateway rule")
		}
		name := "aep-limit-" + resourceSuffix(desired.TenantID()+"/"+item.ID)
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
				threshold["key"] = gatewaypolicy.Pattern(item.Configuration)
				config["rule_items"] = []any{map[string]any{"limit_by_header": "x-aep-limit-keys", "limit_keys": []any{threshold}}}
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

func (r *Reconciler) syncGatewayLimits(ctx context.Context, desired DesiredState) (syncErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.config.ControlURL+"/internal/data-plane/gateway-limits", nil)
	if err != nil {
		return err
	}
	r.addHeaders(request, desired.TenantID())
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return errors.New("native gateway publication unavailable")
	}
	var publication gatewaypolicy.Publication
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&publication); err != nil {
		return err
	}
	if publication.Revision == "" && r.config.NativeGateway.QuotaAdminCredentialRef == nil {
		return nil
	}
	state := "error"
	defer func() {
		if publication.Revision == "" {
			return
		}
		body, _ := json.Marshal(map[string]string{"revision": publication.Revision, "state": state})
		ack, ackErr := http.NewRequestWithContext(ctx, http.MethodPut, r.config.ControlURL+"/internal/data-plane/gateway-limits/status", bytes.NewReader(body))
		if ackErr == nil {
			r.addHeaders(ack, desired.TenantID())
			ack.Header.Set("Content-Type", "application/json")
			result, sendErr := r.config.HTTPClient.Do(ack)
			if sendErr == nil {
				_ = result.Body.Close()
				if result.StatusCode != http.StatusOK && syncErr == nil {
					syncErr = errors.New("native gateway status acknowledgment failed")
				}
			} else if syncErr == nil {
				syncErr = errors.New("native gateway status acknowledgment unavailable")
			}
		}
	}()
	password := ""
	if ref := r.config.NativeGateway.RedisPasswordRef; ref != nil {
		if r.config.CredentialFetcher == nil {
			return errors.New("native Redis secret resolver unavailable")
		}
		password, err = r.config.CredentialFetcher(ctx, *ref)
		if err != nil || password == "" {
			return errors.New("native Redis secret unavailable")
		}
	}
	resources := make([]RenderedResource, 0)
	if publication.Revision != "" {
		resources, err = RenderGatewayLimits(desired, publication, r.config.NativeGateway, password)
	}
	if err != nil {
		return err
	}
	if ref := r.config.NativeGateway.QuotaAdminCredentialRef; ref != nil {
		if r.config.CredentialFetcher == nil {
			return errors.New("native quota secret resolver unavailable")
		}
		credential, fetchErr := r.config.CredentialFetcher(ctx, *ref)
		if fetchErr != nil {
			return errors.New("native quota secret unavailable")
		}
		quota, renderErr := RenderGatewayQuota(desired, r.config.NativeGateway, password, credential)
		if renderErr != nil {
			return renderErr
		}
		resources = append(resources, quota...)
	}
	applier, ok := r.config.Applier.(interface {
		ApplyGatewayNative(context.Context, []RenderedResource) error
	})
	if !ok {
		return errors.New("native gateway requires a Kubernetes applier")
	}
	if err = applier.ApplyGatewayNative(ctx, resources); err != nil {
		return err
	}
	state = "applied"
	return nil
}
