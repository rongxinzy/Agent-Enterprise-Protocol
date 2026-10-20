package gatewayrender

import (
	"encoding/json"
	"testing"
)

func TestPrivateQuotaListenerRetainsNativeGuards(t *testing.T) {
	in, _, cfg := nativeFixture()
	legacy, err := RenderGatewayQuota(in, cfg, "", "disposable-admin")
	if err != nil || len(legacy) != 3 {
		t.Fatalf("legacy quota rendering changed: %v", err)
	}
	cfg.PrivateQuotaListener = true
	resources, err := RenderGatewayQuota(in, cfg, "", "disposable-admin")
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]map[string]any{}
	for _, resource := range resources {
		var obj map[string]any
		if err := json.Unmarshal([]byte(resource.Body), &obj); err != nil {
			t.Fatal(err)
		}
		kind := obj["kind"].(string)
		name := obj["metadata"].(map[string]any)["name"].(string)
		objects[kind+"/"+name] = obj
		if resource.Kind != ResourceGatewayNative || kind == "Ingress" {
			t.Fatal("private quota leaked into the public ingress")
		}
	}
	name := "aep-quota-" + ResourceSuffix(in.Deployment)
	host := name + ".higress-system.svc.cluster.local"
	if len(objects) != 6 {
		t.Fatalf("expected gateway, route, service, native DENY and two native plugins; got %d", len(objects))
	}
	gateway := objects["Gateway/"+name]["spec"].(map[string]any)
	server := gateway["servers"].([]any)[0].(map[string]any)
	if server["port"].(map[string]any)["number"] != float64(18081) || server["hosts"].([]any)[0] != host {
		t.Fatal("management is not on its private host/port")
	}
	service := objects["Service/"+name]["spec"].(map[string]any)
	if service["type"] != "ClusterIP" || service["ports"].([]any)[0].(map[string]any)["port"] != float64(18081) {
		t.Fatal("management service is publicly exposed")
	}
	vs := objects["VirtualService/"+name]["spec"].(map[string]any)
	if vs["gateways"].([]any)[0] != "higress-system/"+name {
		t.Fatal("management is bound to a different gateway")
	}
	http := vs["http"].([]any)[0].(map[string]any)
	if http["directResponse"].(map[string]any)["status"] != float64(404) || http["route"] != nil {
		t.Fatal("management can forward to a model provider")
	}
	guard := objects["AuthorizationPolicy/"+name+"-guard"]["spec"].(map[string]any)
	if guard["action"] != "DENY" {
		t.Fatal("missing independent native DENY guard")
	}
	rule := guard["rules"].([]any)[0].(map[string]any)
	operation := rule["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)
	if operation["ports"].([]any)[0] != "18081" || len(operation["ports"].([]any)) != 1 ||
		operation["hosts"].([]any)[0] != host || operation["hosts"].([]any)[1] != host+":*" {
		t.Fatal("native DENY is not isolated to the private management host and port")
	}
	when := rule["when"].([]any)[0].(map[string]any)
	if when["key"] != "request.headers[authorization]" || when["notValues"].([]any)[0] != "Bearer disposable-admin" {
		t.Fatal("native DENY does not reject the wrong management key")
	}
	for _, pluginName := range []string{name, "aep-quota-auth-" + ResourceSuffix(in.Deployment)} {
		spec := objects["WasmPlugin/"+pluginName]["spec"].(map[string]any)
		if spec["failStrategy"] != "FAIL_CLOSE" || spec["defaultConfigDisable"] != true {
			t.Fatal("native guard is permissive by default")
		}
		found := false
		for _, value := range spec["matchRules"].([]any) {
			rule := value.(map[string]any)
			if domains, ok := rule["domain"].([]any); ok {
				found = len(domains) == 1 && domains[0] == host && rule["configDisable"] == false
			}
		}
		if !found {
			t.Fatal("native guard does not match the private management host")
		}
	}
}

func TestPrivateQuotaListenerIsNotAConfigFileSetting(t *testing.T) {
	var cfg NativeGatewayConfig
	if err := json.Unmarshal([]byte(`{"privateQuotaListener":true,"PrivateQuotaListener":true}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.PrivateQuotaListener {
		t.Fatal("renderer-only stage was enabled through a runtime config file")
	}
}
