package deployment_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAccessOperatorPrivilegesAreExplicitAndNamespaced(t *testing.T) {
	for _, dir := range []string{"services/gateway-operator/config/default", "deploy/kubernetes/operator"} {
		objects := render(t, dir)
		for _, object := range objects {
			if object.GetName() == "gateway-access-operator-role" || object.GetName() == "gateway-access-operator" {
				t.Fatalf("access privileges enabled by default: %s", dir)
			}
		}
	}
	objects := render(t, "services/gateway-operator/config/access-rbac")
	if len(objects) != 2 {
		t.Fatal("optional access privileges must contain only a Role and RoleBinding")
	}
	role := objects["Role/aep-system/gateway-access-operator-role"]
	binding := objects["RoleBinding/aep-system/gateway-access-operator"]
	if role == nil || binding == nil {
		t.Fatal("access permissions escaped the aep-system namespace")
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	for _, raw := range rules {
		rule := raw.(map[string]any)
		resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
		for _, resource := range resources {
			switch resource {
			case "modelaccessgrants", "modelaccessgrants/status", "deploymentconfigs", "authconfigs", "configmaps":
			default:
				t.Fatalf("unexpected resource privilege: %s", resource)
			}
		}
		verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
		for _, verb := range verbs {
			if verb == "delete" || verb == "deletecollection" || verb == "*" {
				t.Fatal("access operator must not delete source history or bypass explicit verbs")
			}
		}
	}
	reference, _, _ := unstructured.NestedString(binding.Object, "roleRef", "kind")
	if reference != "Role" {
		t.Fatal("optional binding grants global privileges")
	}
}
