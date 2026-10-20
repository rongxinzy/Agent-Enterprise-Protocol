package deployment_test

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAccessRelayPermissionsRemainNarrowAndOptIn(t *testing.T) {
	for _, dir := range []string{"operator", "operator-observation"} {
		objects := render(t, "deploy/kubernetes/"+dir)
		role := objects["Role/aep-system/aep-publication-relay"]
		if role == nil {
			t.Fatal("relay role missing")
		}
		rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
		found := false
		for _, raw := range rules {
			rule := raw.(map[string]any)
			resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
			if !slices.Contains(resources, "modelaccessgrants") {
				continue
			}
			groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
			verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
			if !slices.Equal(resources, []string{"modelaccessgrants"}) || !slices.Equal(groups, []string{"gateway.aep.zhiyuan.io"}) ||
				!slices.Equal(verbs, []string{"get", "create", "update"}) {
				t.Fatal("authorization relay grant is broader than delivery needs")
			}
			found = true
		}
		if !found {
			t.Fatal("authorization delivery grant missing")
		}
		configMap := objects["ConfigMap/aep-system/aep-control-service-config"]
		if configMap == nil {
			t.Fatal("control configuration missing")
		}
		config, _, _ := unstructured.NestedStringMap(configMap.Object, "data")
		if config["AEP_GATEWAY_ACCESS_RELAY_ENABLED"] == "true" {
			t.Fatal("operator baseline prematurely enabled experimental access delivery")
		}
	}
	objects := render(t, "deploy/kubernetes/production")
	if objects["Role/aep-system/aep-publication-relay"] != nil {
		t.Fatal("default production baseline gained relay write privileges")
	}
}
