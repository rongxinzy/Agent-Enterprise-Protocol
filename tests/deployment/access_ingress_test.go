package deployment_test

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAccessIngressPrivilegesAndBootstrap(t *testing.T) {
	for _, dir := range []string{"services/gateway-operator/config/default", "deploy/kubernetes/operator"} {
		for _, object := range render(t, dir) {
			if object.GetName() == "aep-access-ingress" || object.GetName() == "aep-access-ingress-operator" {
				t.Fatal("experimental ingress enabled by default")
			}
		}
	}
	objects := render(t, "services/gateway-operator/config/access-ingress-rbac")
	role := objects["Role/higress-system/aep-access-ingress-operator"]
	binding := objects["RoleBinding/higress-system/aep-access-ingress-operator"]
	if len(objects) != 2 || role == nil || binding == nil {
		t.Fatal("session route privileges must be namespaced and independently enabled")
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	for _, raw := range rules {
		resources, _, _ := unstructured.NestedStringSlice(raw.(map[string]any), "resources")
		if len(resources) != 1 || resources[0] != "envoyfilters" {
			t.Fatal("session route writer gained unrelated resource access")
		}
	}
	ref, _, _ := unstructured.NestedString(binding.Object, "roleRef", "kind")
	if ref != "Role" {
		t.Fatal("session route binding grants cluster-wide access")
	}
	objects = render(t, "deploy/kubernetes/access-ingress")
	filter := objects["EnvoyFilter/higress-system/aep-access-ingress"]
	if filter == nil {
		t.Fatal("missing explicit native ingress bootstrap")
	}
	patches, _, _ := unstructured.NestedSlice(filter.Object, "spec", "configPatches")
	found := false
	scrub := false
	adapter := false
	for _, raw := range patches {
		patch := raw.(map[string]any)
		if patch["applyTo"] != "HTTP_FILTER" {
			continue
		}
		op, _, _ := unstructured.NestedString(patch, "patch", "operation")
		name, _, _ := unstructured.NestedString(patch, "patch", "value", "name")
		config, _, _ := unstructured.NestedMap(patch, "patch", "value", "typed_config")
		if name == "envoy.filters.http.header_mutation" {
			after, _, _ := unstructured.NestedString(patch, "match", "listener", "filterChain", "filter", "subFilter", "name")
			if op != "INSERT_AFTER" || after != "envoy.filters.http.ext_authz" {
				t.Fatal("caller bearer must be removed after verification and before provider credential injection")
			}
			mutations, _, _ := unstructured.NestedSlice(config, "mutations", "request_mutations")
			for _, mutation := range mutations {
				if mutation.(map[string]any)["remove"] == "authorization" {
					scrub = true
				}
			}
			data, _ := json.Marshal(config)
			if !strings.Contains(string(data), "remove_on_match") {
				t.Fatal("reserved prefix sanitation is missing")
			}
			continue
		}
		if name == "envoy.filters.http.lua" {
			after, _, _ := unstructured.NestedString(patch, "match", "listener", "filterChain", "filter", "subFilter", "name")
			if op != "INSERT_AFTER" || after != "envoy.filters.http.header_mutation" {
				t.Fatal("trusted identity must be installed after untrusted headers are removed")
			}
			adapter = true
			continue
		}
		found = true
		if op != "INSERT_FIRST" || config["clear_route_cache"] != true || config["failure_mode_allow"] != false {
			t.Fatal("authorization must run before business filters, fail closed and recalculate the authenticated route")
		}
	}
	if !found || !scrub || !adapter {
		t.Fatal("missing authorization filter or pre-provider bearer cleanup")
	}
}
